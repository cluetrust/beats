// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package stats

import (
	"fmt"

	"github.com/elastic/beats/v7/metricbeat/helper"
	"github.com/elastic/beats/v7/metricbeat/mb"
	"github.com/elastic/beats/v7/metricbeat/mb/parse"
	"github.com/elastic/elastic-agent-libs/logp"
)

const (
	defaultScheme = "http"
	defaultPath   = "/streaming/serverz"
)

var (
	hostParser = parse.URLHostParserBuilder{
		DefaultScheme: defaultScheme,
		DefaultPath:   defaultPath,
		PathConfigKey: "stats.metrics_path",
	}.Build()
)

func init() {
	mb.Registry.MustAddMetricSet("stan", "stats", New,
		mb.WithHostParser(hostParser),
		mb.DefaultMetricSet(),
	)
}

// MetricSet holds any configuration or state information. It must implement
// the mb.MetricSet interface. And this is best achieved by embedding
// mb.BaseMetricSet because it implements all of the required mb.MetricSet
// interface methods except for Fetch.
type MetricSet struct {
	mb.BaseMetricSet
	http *helper.HTTP
	Log  *logp.Logger
}

// New creates a new instance of the MetricSet. New is responsible for unpacking
// any MetricSet specific configuration options if there are any.
func New(base mb.BaseMetricSet) (mb.MetricSet, error) {
	config := struct{}{}
	if err := base.Module().UnpackConfig(&config); err != nil {
		return nil, err
	}

	http, err := helper.NewHTTP(base)
	if err != nil {
		return nil, err
	}
	return &MetricSet{
		base,
		http,
		base.Logger().Named("stan"),
	}, nil
}

// Fetch implements the data gathering and data conversion to the right format.
func (m *MetricSet) Fetch(r mb.ReporterV2) error {
	content, err := m.http.FetchContent()
	if err != nil {
		return fmt.Errorf("error in fetch: %w", err)
	}
	err = eventMapping(content, r)
	if err != nil {
		return fmt.Errorf("error in mapping: %w", err)
	}

	return nil
}
