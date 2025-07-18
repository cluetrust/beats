// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package gcs

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/assert"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/option"

	v2 "github.com/elastic/beats/v7/filebeat/input/v2"
	beattest "github.com/elastic/beats/v7/libbeat/publisher/testing"
	"github.com/elastic/beats/v7/x-pack/filebeat/input/gcs/mock"
	conf "github.com/elastic/elastic-agent-libs/config"
	"github.com/elastic/elastic-agent-libs/logp"
)

const (
	bucketGcsTestNew         = "gcs-test-new"
	bucketGcsTestLatest      = "gcs-test-latest"
	beatsMultilineJSONBucket = "beatsmultilinejsonbucket"
	beatsJSONBucket          = "beatsjsonbucket"
	beatsNdJSONBucket        = "beatsndjsonbucket"
	beatsGzJSONBucket        = "beatsgzjsonbucket"
	beatsJSONWithArrayBucket = "beatsjsonwitharraybucket"
	beatsCSVBucket           = "beatscsvbucket"
)

func Test_StorageClient(t *testing.T) {
	tests := []struct {
		name        string
		baseConfig  map[string]interface{}
		mockHandler func() http.Handler
		expected    map[string]bool
		checkJSON   bool
		isError     error
	}{
		{
			name: "SingleBucketWithPoll_NoErr",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                2,
				"poll":                       true,
				"poll_interval":              "5s",
				"buckets": []map[string]interface{}{
					{
						"name": "gcs-test-new",
					},
				},
			},
			mockHandler: mock.GCSServer,
			expected: map[string]bool{
				mock.Gcs_test_new_object_ata_json:      true,
				mock.Gcs_test_new_object_data3_json:    true,
				mock.Gcs_test_new_object_docs_ata_json: true,
			},
		},
		{
			name: "SingleBucketWithoutPoll_NoErr",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                2,
				"poll":                       false,
				"poll_interval":              "10s",
				"buckets": []map[string]interface{}{
					{
						"name": bucketGcsTestNew,
					},
				},
			},
			mockHandler: mock.GCSServer,
			expected: map[string]bool{
				mock.Gcs_test_new_object_ata_json:      true,
				mock.Gcs_test_new_object_data3_json:    true,
				mock.Gcs_test_new_object_docs_ata_json: true,
			},
		},
		{
			name: "TwoBucketsWithPoll_NoErr",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                2,
				"poll":                       true,
				"poll_interval":              "5s",
				"buckets": []map[string]interface{}{
					{
						"name": bucketGcsTestNew,
					},
					{
						"name": bucketGcsTestLatest,
					},
				},
			},
			mockHandler: mock.GCSServer,
			expected: map[string]bool{
				mock.Gcs_test_new_object_ata_json:      true,
				mock.Gcs_test_new_object_data3_json:    true,
				mock.Gcs_test_new_object_docs_ata_json: true,
				mock.Gcs_test_latest_object_ata_json:   true,
				mock.Gcs_test_latest_object_data3_json: true,
			},
		},
		{
			name: "TwoBucketsWithoutPoll_NoErr",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                2,
				"poll":                       false,
				"poll_interval":              "10s",
				"buckets": []map[string]interface{}{
					{
						"name": bucketGcsTestNew,
					},
					{
						"name": bucketGcsTestLatest,
					},
				},
			},
			mockHandler: mock.GCSServer,
			expected: map[string]bool{
				mock.Gcs_test_new_object_ata_json:      true,
				mock.Gcs_test_new_object_data3_json:    true,
				mock.Gcs_test_new_object_docs_ata_json: true,
				mock.Gcs_test_latest_object_ata_json:   true,
				mock.Gcs_test_latest_object_data3_json: true,
			},
		},
		{
			name: "SingleBucketWithPoll_InvalidBucketErr",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                2,
				"poll":                       true,
				"poll_interval":              "5s",
				"buckets": []map[string]interface{}{
					{
						"name": "gcs-test",
					},
				},
			},
			mockHandler: mock.GCSServer,
			expected:    map[string]bool{},
			isError:     errors.New("storage: bucket doesn't exist"),
		},
		{
			name: "SingleBucketWithoutPoll_InvalidBucketErr",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                2,
				"poll":                       false,
				"poll_interval":              "5s",
				"buckets": []map[string]interface{}{
					{
						"name": "gcs-test",
					},
				},
			},
			mockHandler: mock.GCSServer,
			expected:    map[string]bool{},
			isError:     errors.New("storage: bucket doesn't exist"),
		},
		{
			name: "TwoBucketsWithPoll_InvalidBucketErr",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                2,
				"poll":                       true,
				"poll_interval":              "5s",
				"buckets": []map[string]interface{}{
					{
						"name": "gcs-test",
					},
					{
						"name": "gcs-latest",
					},
				},
			},
			mockHandler: mock.GCSServer,
			expected:    map[string]bool{},
			isError:     errors.New("storage: bucket doesn't exist"),
		},
		{
			name: "SingleBucketWithPoll_InvalidConfigValue",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                5100,
				"poll":                       true,
				"poll_interval":              "5s",
				"buckets": []map[string]interface{}{
					{
						"name": "gcs-test",
					},
				},
			},
			mockHandler: mock.GCSServer,
			expected:    map[string]bool{},
			isError:     errors.New("requires value <= 5000 accessing 'max_workers'"),
		},
		{
			name: "TwoBucketWithPoll_InvalidConfigValue",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                5100,
				"poll":                       true,
				"poll_interval":              "5s",
				"buckets": []map[string]interface{}{
					{
						"name": "gcs-test",
					},
					{
						"name": "gcs-latest",
					},
				},
			},
			mockHandler: mock.GCSServer,
			expected:    map[string]bool{},
			isError:     errors.New("requires value <= 5000 accessing 'max_workers'"),
		},
		{
			name: "SingleBucketWithPoll_parseJSON",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                1,
				"poll":                       true,
				"poll_interval":              "5s",
				"parse_json":                 true,
				"buckets": []map[string]interface{}{
					{
						"name": bucketGcsTestLatest,
					},
				},
			},
			mockHandler: mock.GCSServer,
			checkJSON:   true,
			expected: map[string]bool{
				mock.Gcs_test_latest_object_ata_json_parsed:   true,
				mock.Gcs_test_latest_object_data3_json_parsed: true,
			},
		},
		{
			name: "ReadJSON",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                1,
				"poll":                       true,
				"poll_interval":              "5s",
				"buckets": []map[string]interface{}{
					{
						"name": beatsJSONBucket,
					},
				},
			},
			mockHandler: mock.GCSFileServer,
			expected: map[string]bool{
				mock.BeatsFilesBucket_log_json[0]: true,
				mock.BeatsFilesBucket_log_json[1]: true,
				mock.BeatsFilesBucket_log_json[2]: true,
			},
		},
		{
			name: "ReadOctetStreamJSON",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                1,
				"poll":                       true,
				"poll_interval":              "5s",
				"buckets": []map[string]interface{}{
					{
						"name": beatsMultilineJSONBucket,
					},
				},
			},
			mockHandler: mock.GCSFileServer,
			expected: map[string]bool{
				mock.BeatsFilesBucket_multiline_json[0]: true,
				mock.BeatsFilesBucket_multiline_json[1]: true,
			},
		},
		{
			name: "ReadNDJSON",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                1,
				"poll":                       true,
				"poll_interval":              "5s",
				"buckets": []map[string]interface{}{
					{
						"name": beatsNdJSONBucket,
					},
				},
			},
			mockHandler: mock.GCSFileServer,
			expected: map[string]bool{
				mock.BeatsFilesBucket_log_ndjson[0]: true,
				mock.BeatsFilesBucket_log_ndjson[1]: true,
			},
		},
		{
			name: "ReadMultilineGzJSON",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                1,
				"poll":                       true,
				"poll_interval":              "5s",
				"buckets": []map[string]interface{}{
					{
						"name": beatsGzJSONBucket,
					},
				},
			},
			mockHandler: mock.GCSFileServer,
			expected: map[string]bool{
				mock.BeatsFilesBucket_multiline_json_gz[0]: true,
				mock.BeatsFilesBucket_multiline_json_gz[1]: true,
			},
		},
		{
			name: "ReadJSONWithRootAsArray",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                1,
				"poll":                       true,
				"poll_interval":              "5s",
				"buckets": []map[string]interface{}{
					{
						"name": beatsJSONWithArrayBucket,
					},
				},
			},
			mockHandler: mock.GCSFileServer,
			expected: map[string]bool{
				mock.BeatsFilesBucket_json_array[0]: true,
				mock.BeatsFilesBucket_json_array[1]: true,
				mock.BeatsFilesBucket_json_array[2]: true,
				mock.BeatsFilesBucket_json_array[3]: true,
			},
		},
		{
			name: "FilterByTimeStampEpoch",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                1,
				"poll":                       true,
				"poll_interval":              "5s",
				"timestamp_epoch":            1661385600,
				"buckets": []map[string]interface{}{
					{
						"name": bucketGcsTestNew,
					},
				},
			},
			mockHandler: mock.GCSServer,
			expected: map[string]bool{
				mock.Gcs_test_new_object_data3_json:    true,
				mock.Gcs_test_new_object_docs_ata_json: true,
			},
		},
		{
			name: "FilterByFileSelectorRegexSingle",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                1,
				"poll":                       true,
				"poll_interval":              "5s",
				"file_selectors": []map[string]interface{}{
					{
						"regex": "docs/",
					},
				},
				"buckets": []map[string]interface{}{
					{
						"name": bucketGcsTestNew,
					},
				},
			},
			mockHandler: mock.GCSServer,
			expected: map[string]bool{
				mock.Gcs_test_new_object_docs_ata_json: true,
			},
		},
		{
			name: "FilterByFileSelectorRegexMulti",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                1,
				"poll":                       true,
				"poll_interval":              "5s",
				"file_selectors": []map[string]interface{}{
					{
						"regex": "docs/",
					},
					{
						"regex": "data",
					},
				},
				"buckets": []map[string]interface{}{
					{
						"name": bucketGcsTestNew,
					},
				},
			},
			mockHandler: mock.GCSServer,
			expected: map[string]bool{
				mock.Gcs_test_new_object_docs_ata_json: true,
				mock.Gcs_test_new_object_data3_json:    true,
			},
		},
		{
			name: "ExpandEventListFromField",
			baseConfig: map[string]interface{}{
				"project_id":                   "elastic-sa",
				"auth.credentials_file.path":   "testdata/gcs_creds.json",
				"max_workers":                  1,
				"poll":                         true,
				"poll_interval":                "5s",
				"expand_event_list_from_field": "Events",
				"file_selectors": []map[string]interface{}{
					{
						"regex": "events-array",
					},
				},
				"buckets": []map[string]interface{}{
					{
						"name": beatsJSONBucket,
					},
				},
			},
			mockHandler: mock.GCSFileServer,
			expected: map[string]bool{
				mock.BeatsFilesBucket_events_array_json[0]: true,
				mock.BeatsFilesBucket_events_array_json[1]: true,
			},
		},
		{
			name: "MultiContainerWithMultiFileSelectors",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                1,
				"poll":                       true,
				"poll_interval":              "5s",
				"buckets": []map[string]interface{}{
					{
						"name": bucketGcsTestNew,
						"file_selectors": []map[string]interface{}{
							{
								"regex": "docs/",
							},
						},
					},
					{
						"name": bucketGcsTestLatest,
						"file_selectors": []map[string]interface{}{
							{
								"regex": "data_3",
							},
						},
					},
				},
			},
			mockHandler: mock.GCSServer,
			expected: map[string]bool{
				mock.Gcs_test_new_object_docs_ata_json: true,
				mock.Gcs_test_latest_object_data3_json: true,
			},
		},
		{
			name: "FilterByFileSelectorEmptyRegex",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                1,
				"poll":                       true,
				"poll_interval":              "5s",
				"file_selectors": []map[string]interface{}{
					{
						"regex": "",
					},
				},
				"buckets": []map[string]interface{}{
					{
						"name": bucketGcsTestNew,
					},
				},
			},
			mockHandler: mock.GCSServer,
			expected: map[string]bool{
				mock.Gcs_test_new_object_ata_json:      true,
				mock.Gcs_test_new_object_data3_json:    true,
				mock.Gcs_test_new_object_docs_ata_json: true,
			},
		},
		{
			name: "RetryWithDefaultValues",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                1,
				"poll":                       true,
				"poll_interval":              "1m",
				"buckets": []map[string]interface{}{
					{
						"name": "gcs-test-new",
					},
				},
			},
			mockHandler: mock.GCSRetryServer,
			expected: map[string]bool{
				mock.Gcs_test_new_object_ata_json:      true,
				mock.Gcs_test_new_object_data3_json:    true,
				mock.Gcs_test_new_object_docs_ata_json: true,
			},
		},
		{
			name: "RetryWithCustomValues",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                1,
				"poll":                       true,
				"poll_interval":              "10s",
				"retry": map[string]interface{}{
					"max_attempts":             5,
					"initial_backoff_duration": "1s",
					"max_backoff_duration":     "3s",
					"backoff_multiplier":       1.4,
				},
				"buckets": []map[string]interface{}{
					{
						"name": "gcs-test-new",
					},
				},
			},
			mockHandler: mock.GCSRetryServer,
			expected: map[string]bool{
				mock.Gcs_test_new_object_ata_json:      true,
				mock.Gcs_test_new_object_data3_json:    true,
				mock.Gcs_test_new_object_docs_ata_json: true,
			},
		},
		{
			name: "RetryMinimumValueCheck",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                1,
				"poll":                       true,
				"poll_interval":              "10s",
				"retry": map[string]interface{}{
					"max_attempts":             5,
					"initial_backoff_duration": "1s",
					"max_backoff_duration":     "3s",
					"backoff_multiplier":       1,
				},
				"buckets": []map[string]interface{}{
					{
						"name": "gcs-test-new",
					},
				},
			},
			mockHandler: mock.GCSRetryServer,
			expected:    map[string]bool{},
			isError:     errors.New("requires value >= 1.1 accessing 'retry.backoff_multiplier'"),
		},
		{
			name: "BatchSizeGlobal",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"batch_size":                 3,
				"max_workers":                2,
				"poll":                       true,
				"poll_interval":              "5s",
				"buckets": []map[string]interface{}{
					{
						"name": "gcs-test-new",
					},
				},
			},
			mockHandler: mock.GCSServer,
			expected: map[string]bool{
				mock.Gcs_test_new_object_ata_json:      true,
				mock.Gcs_test_new_object_data3_json:    true,
				mock.Gcs_test_new_object_docs_ata_json: true,
			},
		},
		{
			name: "BatchSizeBucketLevel",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                2,
				"poll":                       true,
				"poll_interval":              "5s",
				"buckets": []map[string]interface{}{
					{
						"name":       "gcs-test-new",
						"batch_size": 3,
					},
				},
			},
			mockHandler: mock.GCSServer,
			expected: map[string]bool{
				mock.Gcs_test_new_object_ata_json:      true,
				mock.Gcs_test_new_object_data3_json:    true,
				mock.Gcs_test_new_object_docs_ata_json: true,
			},
		},
		{
			name: "ReadCSV",
			baseConfig: map[string]interface{}{
				"project_id":                 "elastic-sa",
				"auth.credentials_file.path": "testdata/gcs_creds.json",
				"max_workers":                1,
				"poll":                       true,
				"poll_interval":              "10s",
				"decoding.codec.csv.enabled": true,
				"decoding.codec.csv.comma":   " ",
				"buckets": []map[string]interface{}{
					{
						"name": beatsCSVBucket,
					},
				},
			},
			mockHandler: mock.GCSFileServer,
			expected: map[string]bool{
				mock.BeatsFilesBucket_csv[0]: true,
				mock.BeatsFilesBucket_csv[1]: true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serv := httptest.NewServer(tt.mockHandler())
			httpclient := http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						InsecureSkipVerify: true, //nolint:gosec // We can ignore as this is just for testing
					},
				},
			}
			t.Cleanup(serv.Close)

			client, _ := storage.NewClient(context.Background(), option.WithEndpoint(serv.URL), option.WithoutAuthentication(), option.WithHTTPClient(&httpclient))
			cfg := conf.MustNewConfigFrom(tt.baseConfig)
			conf := defaultConfig()
			err := cfg.Unpack(&conf)
			if err != nil {
				assert.EqualError(t, err, fmt.Sprint(tt.isError))
				return
			}
			input := newStatelessInput(conf)

			assert.Equal(t, "gcs-stateless", input.Name())
			assert.NoError(t, input.Test(v2.TestContext{}))

			chanClient := beattest.NewChanClient(len(tt.expected))
			t.Cleanup(func() { _ = chanClient.Close() })

			ctx, cancel := newV2Context(t)
			t.Cleanup(cancel)

			var g errgroup.Group
			g.Go(func() error {
				return input.Run(ctx, chanClient, client)
			})

			var timeout *time.Timer
			if conf.PollInterval != 0 {
				timeout = time.NewTimer(1*time.Second + conf.PollInterval)
			} else {
				timeout = time.NewTimer(5 * time.Second)
			}
			t.Cleanup(func() { _ = timeout.Stop() })

			if len(tt.expected) == 0 {
				if tt.isError != nil && g.Wait() != nil {
					assert.EqualError(t, g.Wait(), tt.isError.Error())
					cancel()
				} else {
					assert.NoError(t, g.Wait())
					cancel()
				}
				return
			}

			var receivedCount int
		wait:
			for {
				select {
				case <-timeout.C:
					t.Errorf("timed out waiting for %d events", len(tt.expected))
					cancel()
					return
				case got := <-chanClient.Channel:
					var val interface{}
					var err error
					if !tt.checkJSON {
						val, err = got.Fields.GetValue("message")
						assert.NoError(t, err)
						assert.True(t, tt.expected[strings.ReplaceAll(val.(string), "\r\n", "\n")])
					} else {
						val, err = got.Fields.GetValue("gcs.storage.object.json_data")
						fVal := fmt.Sprintf("%v", val)
						assert.NoError(t, err)
						assert.True(t, tt.expected[fVal])
					}
					assert.Equal(t, tt.isError, err)
					receivedCount += 1
					if receivedCount == len(tt.expected) {
						cancel()
						break wait
					}
				}
			}
		})
	}
}

func newV2Context(t *testing.T) (v2.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	id, err := generateRandomID(8)
	if err != nil {
		t.Fatalf("failed to generate random id: %v", err)
	}
	return v2.Context{
		Logger:      logp.NewLogger("gcs_test"),
		ID:          "gcs_test-" + id,
		Cancelation: ctx,
	}, cancel
}

func generateRandomID(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
