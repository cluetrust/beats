// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package add_process_metadata

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/elastic/beats/v7/libbeat/beat"
	"github.com/elastic/beats/v7/libbeat/common"
	"github.com/elastic/beats/v7/libbeat/processors"
	jsprocessor "github.com/elastic/beats/v7/libbeat/processors/script/javascript/module/processor/registry"
	conf "github.com/elastic/elastic-agent-libs/config"
	"github.com/elastic/elastic-agent-libs/logp"
	"github.com/elastic/elastic-agent-libs/mapstr"
	"github.com/elastic/elastic-agent-system-metrics/metric/system/cgroup"
	"github.com/elastic/elastic-agent-system-metrics/metric/system/resolve"
	"github.com/elastic/go-sysinfo"
)

const (
	processorName       = "add_process_metadata"
	cacheExpiration     = time.Second * 30
	cacheCapacity       = 32 << 10 // maximum number of process cache entries.
	cacheEvictionEffort = 10       // number of entries to sample for expiry eviction.
)

var (
	// ErrNoMatch is returned when the event doesn't contain any of the fields
	// specified in match_pids.
	ErrNoMatch = errors.New("none of the fields in match_pids found in the event")

	// ErrNoProcess is returned when metadata for a process can't be collected.
	ErrNoProcess = errors.New("process not found")

	procCache = newProcessCache(cacheExpiration, cacheCapacity, cacheEvictionEffort, gosysinfoProvider{})

	// cgroups resolver, turned to a stub function to make testing easier.
	initCgroupPaths processors.InitCgroupHandler = func(rootfsMountpoint resolve.Resolver, ignoreRootCgroups bool) (processors.CGReader, error) {
		return cgroup.NewReader(rootfsMountpoint, ignoreRootCgroups)
	}

	instanceID atomic.Uint32
)

type addProcessMetadata struct {
	config       config
	provider     processMetadataProvider
	cgroupsCache *common.Cache
	cidProvider  cidProvider
	log          *logp.Logger
	mappings     mapstr.M
	uniqueID     []byte
}

type processMetadata struct {
	entityID                           string
	name, title, exe, username, userid string
	args                               []string
	env                                map[string]string
	startTime                          time.Time
	pid, ppid                          int
	groupname, groupid                 string
	capEffective, capPermitted         []string
	fields                             mapstr.M
}

type processMetadataProvider interface {
	GetProcessMetadata(pid int) (*processMetadata, error)
}

type cidProvider interface {
	GetCid(pid int) (string, error)
}

func init() {
	processors.RegisterPlugin(processorName, NewWithCache)
	jsprocessor.RegisterPlugin("AddProcessMetadata", New)
}

// New constructs a new add_process_metadata processor.
func New(cfg *conf.C, log *logp.Logger) (beat.Processor, error) {
	config := defaultConfig()
	if err := cfg.Unpack(&config); err != nil {
		return nil, fmt.Errorf("fail to unpack the %v configuration: %w", processorName, err)
	}

	return newProcessMetadataProcessorWithProvider(config, &procCache, false)
}

// NewWithCache construct a new add_process_metadata processor with cache for container IDs.
// Resulting processor implements `Close()` to release the cache resources.
func NewWithCache(cfg *conf.C, log *logp.Logger) (beat.Processor, error) {
	config := defaultConfig()
	if err := cfg.Unpack(&config); err != nil {
		return nil, fmt.Errorf("fail to unpack the %v configuration: %w", processorName, err)
	}

	return newProcessMetadataProcessorWithProvider(config, &procCache, true)
}

func NewWithConfig(opts ...ConfigOption) (beat.Processor, error) {
	cfg := defaultConfig()

	for _, o := range opts {
		o(&cfg)
	}

	return newProcessMetadataProcessorWithProvider(cfg, &procCache, true)
}

func newProcessMetadataProcessorWithProvider(config config, provider processMetadataProvider, withCache bool) (proc beat.Processor, err error) {
	// Logging (each processor instance has a unique ID).
	var (
		id  = int(instanceID.Add(1))
		log = logp.NewLogger(processorName).With("instance_id", id)
	)

	// If neither option is configured, then add a default. A default cgroup_regex
	// cannot be added to the struct returned by defaultConfig() because if
	// config_regex is set, it would take precedence over any user-configured
	// cgroup_prefixes.
	hasCgroupPrefixes := len(config.CgroupPrefixes) > 0
	hasCgroupRegex := config.CgroupRegex != nil
	if !hasCgroupPrefixes && !hasCgroupRegex {
		config.CgroupRegex = defaultCgroupRegex
	}

	mappings, err := config.getMappings()
	if err != nil {
		return nil, fmt.Errorf("error unpacking %v.target_fields: %w", processorName, err)
	}

	p := addProcessMetadata{
		config:   config,
		provider: provider,
		log:      log,
		mappings: mappings,
	}

	if host, _ := sysinfo.Host(); host != nil {
		if uniqueID := host.Info().UniqueID; uniqueID != "" {
			p.uniqueID = []byte(uniqueID)
		}
	}

	reader, err := initCgroupPaths(resolve.NewTestResolver(config.HostPath), false)
	if errors.Is(err, cgroup.ErrCgroupsMissing) {
		reader = &processors.NilCGReader{}
	} else if err != nil {
		return nil, fmt.Errorf("error creating cgroup reader: %w", err)
	}

	// don't use cgroup.ProcessCgroupPaths to save it from doing the work when container id disabled
	if ok := containsValue(mappings, "container.id"); ok {
		if withCache && config.CgroupCacheExpireTime != 0 {
			p.log.Debug("Initializing cgroup cache")
			evictionListener := func(k common.Key, v common.Value) {
				p.log.Debugf("Evicted cached cgroups for PID=%v", k)
			}

			p.cgroupsCache = common.NewCacheWithRemovalListener(config.CgroupCacheExpireTime, 100, evictionListener)
			p.cgroupsCache.StartJanitor(config.CgroupCacheExpireTime)
			p.cidProvider = newCidProvider(config.CgroupPrefixes, config.CgroupRegex, reader, p.cgroupsCache)
		} else {
			p.cidProvider = newCidProvider(config.CgroupPrefixes, config.CgroupRegex, reader, nil)
		}
	}

	if withCache {
		return &addProcessMetadataCloser{p}, nil
	}

	return &p, nil
}

// check if the value exist in mapping
func containsValue(m mapstr.M, v string) bool {
	for _, x := range m {
		if x == v {
			return true
		}
	}
	return false
}

// Run enriches the given event with the host meta data.
func (p *addProcessMetadata) Run(event *beat.Event) (*beat.Event, error) {
	for _, pidField := range p.config.MatchPIDs {
		result, err := p.enrich(event, pidField)
		if err != nil {
			switch {
			case errors.Is(err, mapstr.ErrKeyNotFound):
				continue
			case errors.Is(err, ErrNoProcess):
				return event, err
			default:
				return event, fmt.Errorf("error applying %s processor: %w", processorName, err)
			}
		}
		if result != nil {
			event = result
		}
		return event, nil
	}
	if p.config.IgnoreMissing {
		return event, nil
	}
	return event, ErrNoMatch
}

func pidToInt(value interface{}) (pid int, err error) {
	switch v := value.(type) {
	case string:
		pid, err = strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("error converting string to integer: %w", err)
		}
	case int:
		pid = v
	case int8, int16, int32, int64:
		pid64 := reflect.ValueOf(v).Int()
		if pid = int(pid64); int64(pid) != pid64 {
			return 0, fmt.Errorf("integer out of range: %d", pid64)
		}
	case uint, uintptr, uint8, uint16, uint32, uint64:
		pidu64 := reflect.ValueOf(v).Uint()
		if pid = int(pidu64); pid < 0 || uint64(pid) != pidu64 {
			return 0, fmt.Errorf("integer out of range: %d", pidu64)
		}
	default:
		return 0, fmt.Errorf("not an integer or string, but %T", v)
	}
	return pid, nil
}

func (p *addProcessMetadata) enrich(event *beat.Event, pidField string) (result *beat.Event, err error) {
	pidIf, err := event.GetValue(pidField)
	if err != nil {
		return nil, err
	}

	pid, err := pidToInt(pidIf)
	if err != nil {
		return nil, fmt.Errorf("cannot parse pid field '%s': %w", pidField, err)
	}

	var meta mapstr.M

	metaPtr, err := p.provider.GetProcessMetadata(pid)
	if err != nil || metaPtr == nil {
		// no process metadata, lets still try to get container id
		p.log.Debugf("failed to get process metadata for PID=%d: %v", pid, err)
		meta = mapstr.M{}
	} else {
		meta = metaPtr.fields
	}

	cid, err := p.getContainerID(pid)
	if cid == "" || err != nil {
		p.log.Debugf("failed to get container id for PID=%d: %v", pid, err)
	} else {
		if _, err = meta.Put("container", mapstr.M{"id": cid}); err != nil {
			return nil, err
		}
	}

	if len(meta) == 0 {
		// no metadata nor container id
		return nil, ErrNoProcess
	}

	result = event.Clone()
	for dest, sourceIf := range p.mappings {
		source, castOk := sourceIf.(string)
		if !castOk {
			// Should never happen, as source is generated by Config.prepareMappings()
			return nil, errors.New("source is not a string")
		}
		if !p.config.OverwriteKeys {
			if _, err := result.GetValue(dest); err == nil {
				return nil, fmt.Errorf("target field '%s' already exists and overwrite_keys is false", dest)
			}
		}

		value, err := meta.GetValue(source)
		if err != nil {
			// skip missing values
			continue
		}

		if _, err = result.PutValue(dest, value); err != nil {
			return nil, err
		}
	}

	return result, nil
}

func (p *addProcessMetadata) getContainerID(pid int) (string, error) {
	if p.cidProvider == nil {
		return "", nil
	}
	cid, err := p.cidProvider.GetCid(pid)
	if err != nil {
		return "", err
	}
	return cid, nil
}

type addProcessMetadataCloser struct {
	addProcessMetadata
}

func (p *addProcessMetadataCloser) Close() error {
	if p.addProcessMetadata.cgroupsCache != nil {
		p.addProcessMetadata.cgroupsCache.StopJanitor()
	}
	return nil
}

// String returns the processor representation formatted as a string
func (p *addProcessMetadata) String() string {
	return fmt.Sprintf("%v=[match_pids=%v, mappings=%v, ignore_missing=%v, overwrite_fields=%v, restricted_fields=%v, host_path=%v, cgroup_prefixes=%v]",
		processorName, p.config.MatchPIDs, p.mappings, p.config.IgnoreMissing,
		p.config.OverwriteKeys, p.config.RestrictedFields, p.config.HostPath, p.config.CgroupPrefixes)
}

func (p *processMetadata) toMap() mapstr.M {
	process := mapstr.M{
		"entity_id":  p.entityID,
		"name":       p.name,
		"title":      p.title,
		"executable": p.exe,
		"args":       p.args,
		"env":        p.env,
		"pid":        p.pid,
		"parent": mapstr.M{
			"pid": p.ppid,
		},
		"start_time": p.startTime,
	}
	if p.username != "" || p.userid != "" {
		user := mapstr.M{}
		if p.username != "" {
			user["name"] = p.username
		}
		if p.userid != "" {
			user["id"] = p.userid
		}
		process["owner"] = user
	}
	if len(p.capEffective) > 0 {
		process.Put("thread.capabilities.effective", p.capEffective)
	}
	if len(p.capPermitted) > 0 {
		process.Put("thread.capabilities.permitted", p.capPermitted)
	}
	if p.groupname != "" || p.groupid != "" {
		group := mapstr.M{}
		if p.groupname != "" {
			group["name"] = p.groupname
		}
		if p.groupid != "" {
			group["id"] = p.groupid
		}
		process["group"] = group
	}

	return mapstr.M{
		"process": process,
	}
}
