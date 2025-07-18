// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

//go:build !requirefips

package gcppubsub

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/elastic/beats/v7/filebeat/harvester"
	"github.com/elastic/beats/v7/libbeat/common"
	"github.com/elastic/elastic-agent-libs/transport/httpcommon"

	"cloud.google.com/go/pubsub"
	"golang.org/x/oauth2/google"
)

type config struct {
	harvester.ForwarderConfig `config:",inline"`
	// Google Cloud project name.
	ProjectID string `config:"project_id" validate:"required"`

	// Google Cloud Pub/Sub topic name.
	Topic string `config:"topic" validate:"required"`

	// Google Cloud Pub/Sub subscription name. Multiple Filebeats can pull from same subscription.
	Subscription struct {
		Name                   string `config:"name" validate:"required"`
		NumGoroutines          int    `config:"num_goroutines"`
		MaxOutstandingMessages int    `config:"max_outstanding_messages"`
		Create                 bool   `config:"create"`
	} `config:"subscription"`

	// JSON file containing authentication credentials and key.
	CredentialsFile string `config:"credentials_file"`

	// JSON blob containing authentication credentials and key.
	CredentialsJSON common.JSONBlob `config:"credentials_json"`

	// Overrides the default Pub/Sub service address and disables TLS. For testing.
	AlternativeHost string `config:"alternative_host"`

	Transport httpTransportSettings `config:",inline"`
}

// httpTransportSettings is the proxy configuration subset of httpcommon.HTTPTransportSettings.
// It is used to allow configuration of proxies without promising other configuration
// options from that type.
type httpTransportSettings struct {
	Proxy httpcommon.HTTPClientProxySettings `config:",inline" yaml:",inline"`
}

func (c *config) Validate() error {
	if c.AlternativeHost != "" && !c.Transport.Proxy.Disable && c.Transport.Proxy.URL != nil {
		return errors.New("alternative_host may not be configured with a proxy")
	}

	// credentials_file
	if c.CredentialsFile != "" {
		if _, err := os.Stat(c.CredentialsFile); os.IsNotExist(err) {
			return fmt.Errorf("credentials_file is configured, but the file %q cannot be found", c.CredentialsFile)
		} else {
			return nil
		}
	}

	// credentials_json
	if len(c.CredentialsJSON) > 0 {
		return nil
	}

	// Application Default Credentials (ADC)
	ctx := context.Background()
	if _, err := google.FindDefaultCredentials(ctx, pubsub.ScopePubSub); err == nil {
		return nil
	}

	return fmt.Errorf("no authentication credentials were configured or detected " +
		"(credentials_file, credentials_json, and application default credentials (ADC))")
}

func defaultConfig() config {
	var c config
	c.ForwarderConfig = harvester.ForwarderConfig{
		Type: "gcp-pubsub",
	}
	c.Subscription.NumGoroutines = 1
	// The input gets blocked until flush.min_events or flush.timeout is reached.
	// Hence max_outstanding_message has to be at least flush.min_events to avoid this blockage.
	c.Subscription.MaxOutstandingMessages = 1600
	c.Subscription.Create = true
	c.Transport.Proxy = httpcommon.DefaultHTTPClientProxySettings()
	return c
}
