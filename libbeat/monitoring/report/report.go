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

package report

import (
	"errors"
	"fmt"

	"github.com/elastic/beats/v7/libbeat/beat"
	conf "github.com/elastic/elastic-agent-libs/config"
)

type config struct {
	// allow for maximum one reporter being configured
	Reporter conf.Namespace `config:",inline"`
}

type Settings struct {
	DefaultUsername string
	ClusterUUID     string
}

type Reporter interface {
	Stop()
}

type ReporterFactory func(beat.Info, beat.Monitoring, Settings, *conf.C) (Reporter, error)

type hostsCfg struct {
	Hosts []string `config:"hosts"`
}

var (
	defaultConfig = config{}

	reportFactories = map[string]ReporterFactory{}
)

func RegisterReporterFactory(name string, f ReporterFactory) {
	if reportFactories[name] != nil {
		panic(fmt.Sprintf("Reporter '%v' already registered", name))
	}
	reportFactories[name] = f
}

func New(
	beat beat.Info,
	mon beat.Monitoring,
	settings Settings,
	cfg *conf.C,
	outputs conf.Namespace,
) (Reporter, error) {
	name, cfg, err := getReporterConfig(cfg, outputs)
	if err != nil {
		return nil, err
	}

	f := reportFactories[name]
	if f == nil {
		return nil, fmt.Errorf("unknown reporter type '%v'", name)
	}

	return f(beat, mon, settings, cfg)
}

func getReporterConfig(
	monitoringConfig *conf.C,
	outputs conf.Namespace,
) (string, *conf.C, error) {
	cfg := collectSubObject(monitoringConfig)
	config := defaultConfig
	if err := cfg.Unpack(&config); err != nil {
		return "", nil, err
	}

	// load reporter from `monitoring` section and optionally
	// merge with output settings
	if config.Reporter.IsSet() {
		name := config.Reporter.Name()
		rc := config.Reporter.Config()

		// merge reporter config with output config if both are present
		if outCfg := outputs.Config(); outputs.Name() == name && outCfg != nil {
			merged, err := conf.MergeConfigs(outCfg, rc)
			if err != nil {
				return "", nil, err
			}

			// Make sure hosts from reporter configuration get precedence over hosts
			// from output configuration
			if err := mergeHosts(merged, outCfg, rc); err != nil {
				return "", nil, err
			}

			rc = merged
		}

		return name, rc, nil
	}

	// find output also available for reporting telemetry.
	if outputs.IsSet() {
		name := outputs.Name()
		if reportFactories[name] != nil {
			return name, outputs.Config(), nil
		}
	}

	return "", nil, errors.New("No monitoring reporter configured")
}

func collectSubObject(cfg *conf.C) *conf.C {
	out := conf.NewConfig()
	for _, field := range cfg.GetFields() {
		if obj, err := cfg.Child(field, -1); err == nil {
			// on error field is no object, but primitive value -> ignore
			out.SetChild(field, -1, obj) //nolint:errcheck // this error is safe to ignore
			continue
		}
	}
	return out
}

func mergeHosts(merged, outCfg, reporterCfg *conf.C) error {
	if merged == nil {
		merged = conf.NewConfig()
	}

	outputHosts := hostsCfg{}
	if outCfg != nil {
		if err := outCfg.Unpack(&outputHosts); err != nil {
			return fmt.Errorf("unable to parse hosts from output config: %w", err)
		}
	}

	reporterHosts := hostsCfg{}
	if reporterCfg != nil {
		if err := reporterCfg.Unpack(&reporterHosts); err != nil {
			return fmt.Errorf("unable to parse hosts from reporter config: %w", err)
		}
	}

	if len(outputHosts.Hosts) == 0 && len(reporterHosts.Hosts) == 0 {
		return nil
	}

	// Give precedence to reporter hosts over output hosts
	var newHostsCfg *conf.C
	var err error
	if len(reporterHosts.Hosts) > 0 {
		newHostsCfg, err = conf.NewConfigFrom(reporterHosts.Hosts)
	} else {
		newHostsCfg, err = conf.NewConfigFrom(outputHosts.Hosts)
	}
	if err != nil {
		return fmt.Errorf("unable to make config from new hosts: %w", err)
	}

	if err := merged.SetChild("hosts", -1, newHostsCfg); err != nil {
		return fmt.Errorf("unable to set new hosts into merged config: %w", err)
	}
	return nil
}
