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

package conditions

import (
	"errors"

	"github.com/elastic/beats/v7/libbeat/common/match"
	"github.com/elastic/elastic-agent-libs/logp"
)

const logName = "conditions"

// Config represents a configuration for a condition, as you would find it in the config files.
type Config struct {
	Equals    *Fields                `config:"equals"`
	Contains  *Fields                `config:"contains"`
	Regexp    *Fields                `config:"regexp"`
	Range     *Fields                `config:"range"`
	HasFields []string               `config:"has_fields"`
	Network   map[string]interface{} `config:"network"`
	OR        []Config               `config:"or"`
	AND       []Config               `config:"and"`
	NOT       *Config                `config:"not"`
}

// Condition is the interface for all defined conditions
type Condition interface {
	Check(event ValuesMap) bool
	String() string
}

// ValuesMap provides a common interface to read matchers for condition checking
type ValuesMap interface {
	// GetValue returns the given field from the map
	GetValue(string) (interface{}, error)
}

// NewCondition takes a Config and turns it into a real Condition
func NewCondition(config *Config, logger *logp.Logger) (Condition, error) {
	if config == nil {
		// empty condition
		return nil, errors.New("missing condition config")
	}

	var condition Condition
	var err error
	switch {
	case config.Equals != nil:
		condition, err = NewEqualsCondition(config.Equals.fields, logger)
	case config.Contains != nil:
		condition, err = NewMatcherCondition("contains", config.Contains.fields, match.CompileString, logger)
	case config.Regexp != nil:
		condition, err = NewMatcherCondition("regexp", config.Regexp.fields, match.Compile, logger)
	case config.Range != nil:
		condition, err = NewRangeCondition(config.Range.fields, logger)
	case config.HasFields != nil:
		condition = NewHasFieldsCondition(config.HasFields)
	case config.Network != nil && len(config.Network) > 0:
		condition, err = NewNetworkCondition(config.Network, logger)
	case len(config.OR) > 0:
		var conditionsList []Condition
		conditionsList, err = NewConditionList(config.OR, logger)
		condition = NewOrCondition(conditionsList)
	case len(config.AND) > 0:
		var conditionsList []Condition
		conditionsList, err = NewConditionList(config.AND, logger)
		condition = NewAndCondition(conditionsList)
	case config.NOT != nil:
		var inner Condition
		inner, err = NewCondition(config.NOT, logger)
		if err == nil {
			condition, err = NewNotCondition(inner)
		}
	default:
		err = errors.New("missing or invalid condition")
	}
	if err != nil {
		return nil, err
	}

	logger.Named(logName).Debugf("New condition %v", condition)
	return condition, nil
}

// NewConditionList takes a slice of Config objects and turns them into real Condition objects.
func NewConditionList(config []Config, logger *logp.Logger) ([]Condition, error) {
	out := make([]Condition, len(config))
	for i := range config {
		cond, err := NewCondition(&config[i], logger)
		if err != nil {
			return nil, err
		}

		out[i] = cond
	}
	return out, nil
}
