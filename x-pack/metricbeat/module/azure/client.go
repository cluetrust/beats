// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

//go:build !requirefips

package azure

import (
	"fmt"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/monitor/armmonitor"

	"github.com/elastic/beats/v7/metricbeat/mb"
	"github.com/elastic/elastic-agent-libs/logp"
)

// MetricCollectionInfo contains information about the last time
// a metric was collected and the time grain used.
type MetricCollectionInfo struct {
	timestamp time.Time
	timeGrain string
}

// BaseClient represents the base azure client which will make use of the azure sdk go metrics related clients
// It implements all the common methods between Client and BatchClient
type BaseClient struct {
	AzureMonitorService Service
	Config              Config
	Log                 *logp.Logger
	Resources           []Resource
	MetricRegistry      *MetricRegistry
}

// Client represents the azure client which will make use of the azure sdk go metrics related clients
type Client struct {
	*BaseClient
	ResourceConfigurations ResourceConfiguration
}

// mapResourceMetrics function type will map the configuration options to client metrics (depending on the metricset)
type mapResourceMetrics func(client *Client, resources []*armresources.GenericResourceExpanded, resourceConfig ResourceConfig) ([]Metric, error)

// NewClient instantiates the Azure monitoring client
func NewClient(config Config) (*Client, error) {
	azureMonitorService, err := NewService(config)
	if err != nil {
		return nil, err
	}

	logger := logp.NewLogger("azure monitor client")

	client := &Client{
		BaseClient: &BaseClient{
			AzureMonitorService: azureMonitorService,
			Config:              config,
			Log:                 logger,
			MetricRegistry:      NewMetricRegistry(logger),
		},
	}

	client.ResourceConfigurations.RefreshInterval = config.RefreshListInterval

	return client, nil
}

// InitResources function will retrieve and validate the resources configured by the users and then map the information configured to client metrics.
// the mapMetric function sent in this case will handle the mapping part as different metric and aggregation options work for different metricsets
func (client *Client) InitResources(fn mapResourceMetrics) error {
	if len(client.Config.Resources) == 0 {
		return fmt.Errorf("no resource options defined")
	}

	// check if refresh interval has been set and if it has expired
	if !client.ResourceConfigurations.Expired() {
		return nil
	}

	var metrics []Metric
	//reset client resources
	client.Resources = []Resource{}
	for _, resource := range client.Config.Resources {
		// retrieve azure resources information
		resourceList, err := client.AzureMonitorService.GetResourceDefinitions(resource.Id, resource.Group, resource.Type, resource.Query)
		if err != nil {
			err = fmt.Errorf("failed to retrieve resources: %w", err)
			return err
		}

		if len(resourceList) == 0 {
			err = fmt.Errorf("failed to retrieve resources: No resources returned using the configuration options resource ID %s, resource group %s, resource type %s, resource query %s",
				resource.Id, resource.Group, resource.Type, resource.Query)
			client.Log.Error(err)
			continue
		}

		// Map resources to the client
		for _, resource := range resourceList {
			if !containsResource(*resource.ID, client.Resources) {
				client.Resources = append(client.Resources, Resource{
					Id:           *resource.ID,
					Name:         *resource.Name,
					Location:     *resource.Location,
					Type:         *resource.Type,
					Group:        getResourceGroupFromId(*resource.ID),
					Tags:         mapTags(resource.Tags),
					Subscription: client.Config.SubscriptionId})
			}
		}

		// Collects and stores metrics definitions for the cloud resources.
		resourceMetrics, err := fn(client, resourceList, resource)
		if err != nil {
			return err
		}

		metrics = append(metrics, resourceMetrics...)
	}
	// users could add or remove resources while metricbeat is running so we could encounter the situation where resources are unavailable we log an error message (see above)
	// we also log a debug message when absolutely no resources are found
	if len(metrics) == 0 {
		client.Log.Debug("no resources were found based on all the configurations options entered")
	}
	client.ResourceConfigurations.Metrics = metrics

	return nil
}

// GetMetricValues returns the metric values for the given cloud resources.
func (client *Client) GetMetricValues(referenceTime time.Time, metrics []Metric, reporter mb.ReporterV2) []Metric {
	var result []Metric

	for _, metric := range metrics {
		startTime, endTime := calculateTimespan(referenceTime, metric.TimeGrain, client.Config)
		timespan := fmt.Sprintf("%s/%s", startTime.Format(time.RFC3339), endTime.Format(time.RFC3339))

		//
		// Before fetching the metric values, check if the metric
		// has been collected within the time grain.
		//
		// Why do we need this?
		//
		// Some metricsets contains metrics with long time grains (e.g. 1 hour).
		//
		// If we collect the metric values every 5 minutes, we will end up fetching
		// the same data over and over again for all metrics with a time grain
		// larger than 5 minutes.
		//
		// The registry keeps track of the last timestamp the metricset collected
		// the metric values and the time grain used.
		//
		// By comparing the last collection time with the current time, and
		// the time grain of the metric, we can determine if the metric needs
		// to be collected again, or if we can skip it.
		//
		if !client.MetricRegistry.NeedsUpdate(referenceTime, metric) {
			continue
		}

		// build the 'filter' parameter which will contain any dimensions configured
		var filter string
		if len(metric.Dimensions) > 0 {
			var filterList []string
			for _, dim := range metric.Dimensions {
				filterList = append(filterList, dim.Name+" eq '"+dim.Value+"'")
			}
			filter = strings.Join(filterList, " AND ")
		}

		// Fetch the metric values from the Azure API.
		resp, timeGrain, err := client.AzureMonitorService.GetMetricValues(
			metric.ResourceSubId,
			metric.Namespace,
			metric.TimeGrain,
			timespan,
			metric.Names,
			metric.Aggregations,
			filter,
		)
		if err != nil {
			err = fmt.Errorf("error while listing metric values by resource ID %s and namespace  %s: %w", metric.ResourceSubId, metric.Namespace, err)
			client.Log.Error(err)
			reporter.Error(err)

			// Skip this metric and continue with the next one.
			break
		}

		// Update the metric registry with the latest timestamp and
		// time grain for each metric.
		//
		// We track the time grain Azure used for this metric values from
		// the API response.
		client.MetricRegistry.Update(metric, MetricCollectionInfo{
			timeGrain: timeGrain,
			timestamp: referenceTime,
		})

		for i, currentMetric := range client.ResourceConfigurations.Metrics {
			if matchMetrics(currentMetric, metric) {
				// Map the metric values from the API response.
				current := mapMetricValues(resp, currentMetric.Values)
				client.ResourceConfigurations.Metrics[i].Values = current

				// Some predefined metricsets configuration do not have a time grain.
				// Here is an example:
				// https://github.com/elastic/beats/blob/024a9cec6608c6f371ad1cb769649e024124ff92/x-pack/metricbeat/module/azure/database_account/manifest.yml#L11-L13
				//
				// Predefined metricsets sometimes have long lists of metrics
				// with no time grains. Or users can configure their own
				// custom metricsets with no time grain.
				//
				// In this case, we track the time grain returned by the API. Azure
				// provides a default time grain for each metric.
				if client.ResourceConfigurations.Metrics[i].TimeGrain == "" {
					client.ResourceConfigurations.Metrics[i].TimeGrain = timeGrain
				}

				result = append(result, client.ResourceConfigurations.Metrics[i])
			}
		}
	}

	return result
}

// CreateMetric function will create a client metric based on the resource and metrics configured
func (client *Client) CreateMetric(resourceId string, subResourceId string, namespace string, metrics []string, aggregations string, dimensions []Dimension, timeGrain string) Metric {
	if subResourceId == "" {
		subResourceId = resourceId
	}
	met := Metric{
		ResourceId:    resourceId,
		ResourceSubId: subResourceId,
		Namespace:     namespace,
		Names:         metrics,
		Dimensions:    dimensions,
		Aggregations:  aggregations,
		TimeGrain:     timeGrain,
	}

	for _, prevMet := range client.ResourceConfigurations.Metrics {
		if len(prevMet.Values) != 0 && matchMetrics(prevMet, met) {
			met.Values = prevMet.Values
		}
	}

	return met
}

// MapMetricByPrimaryAggregation will map the primary aggregation of the metric definition to the client metric
func (client *Client) MapMetricByPrimaryAggregation(metrics []armmonitor.MetricDefinition, resourceId string, subResourceId string, namespace string, dim []Dimension, timeGrain string) []Metric {
	clientMetrics := make([]Metric, 0)
	metricGroups := make(map[string][]armmonitor.MetricDefinition)

	for _, met := range metrics {
		metricGroups[string(*met.PrimaryAggregationType)] = append(metricGroups[string(*met.PrimaryAggregationType)], met)
	}

	for key, metricGroup := range metricGroups {
		var metricNames []string
		for _, metricName := range metricGroup {
			metricNames = append(metricNames, *metricName.Name.Value)
		}
		clientMetrics = append(clientMetrics, client.CreateMetric(resourceId, subResourceId, namespace, metricNames, key, dim, timeGrain))
	}

	return clientMetrics
}

// GetVMForMetadata func will retrieve the VM details in order to fill in the cloud metadata
// and also update the client resources
func (client *BaseClient) GetVMForMetadata(resource *Resource, referencePoint KeyValuePoint) VmResource {
	var (
		vm           VmResource
		resourceName = resource.Name
		resourceId   = resource.Id
	)

	// Search the dimensions for the "VMName" dimension. This dimension is present for VM Scale Sets.
	if dimensionValue, ok := getDimension("VMName", referencePoint.Dimensions); ok {
		instanceId := getInstanceId(dimensionValue)
		if instanceId != "" {
			resourceId += fmt.Sprintf("/virtualMachines/%s", instanceId)
			resourceName = dimensionValue
		}
	}

	// if vm has been already added to the resource then it should be returned
	if existingVM, ok := getVM(resourceName, resource.Vms); ok {
		return existingVM
	}

	// an additional call is necessary in order to retrieve the vm specific details
	expandedResource, err := client.AzureMonitorService.GetResourceDefinitionById(resourceId)
	if err != nil {
		client.Log.Error(err, "could not retrieve the resource details by resource ID %s", resourceId)
		return VmResource{}
	}

	vm.Name = *expandedResource.Name

	if expandedResource.Properties != nil {
		if properties, ok := expandedResource.Properties.(map[string]interface{}); ok {
			if hardware, ok := properties["hardwareProfile"]; ok {
				if vmSz, ok := hardware.(map[string]interface{}); ok {
					if vmSz, ok := vmSz["vmSize"]; ok {
						vm.Size, _ = vmSz.(string)
					}
				}
				if vmID, ok := properties["vmId"]; ok {
					vm.Id, _ = vmID.(string)
				}
			}
		}
	}

	if len(vm.Size) == 0 && expandedResource.SKU != nil && expandedResource.SKU.Name != nil {
		vm.Size = *expandedResource.SKU.Name
	}

	// the client resource and selected resources are being updated in order to avoid additional calls
	client.AddVmToResource(resource.Id, vm)

	resource.Vms = append(resource.Vms, vm)

	return vm
}

// GetResourceForMetaData will retrieve resource details for the selected metric configuration
func (client *BaseClient) GetResourceForMetaData(grouped Metric) Resource {
	for _, res := range client.Resources {
		if res.Id == grouped.ResourceId {
			return res
		}
	}
	return Resource{}
}

func (client *BaseClient) LookupResource(resourceId string) Resource {
	for _, res := range client.Resources {
		if res.Id == resourceId {
			return res
		}
	}
	return Resource{}
}

// AddVmToResource will add the vm details to the resource
func (client *BaseClient) AddVmToResource(resourceId string, vm VmResource) {
	if len(vm.Id) > 0 && len(vm.Name) > 0 {
		for i, res := range client.Resources {
			if res.Id == resourceId {
				client.Resources[i].Vms = append(client.Resources[i].Vms, vm)
			}
		}
	}
}

// mapToEvents maps the metric values to events and reports them to Elasticsearch.
func (client *BaseClient) MapToEvents(metrics []Metric, reporter mb.ReporterV2) error {

	// Map the metric values into a list of key/value points.
	//
	// This makes it easier to group the metrics by timestamp
	// and dimensions.
	points := mapToKeyValuePoints(metrics)

	// Group the points by timestamp and other fields we consider
	// as dimensions for the whole event.
	//
	// Metrics have their own dimensions, and this is fine at the
	// metric level.
	//
	// We identified a set of field we consider as dimensions
	// at the event level. The event level dimensions define
	// the time series when TSDB is enabled.
	groupedPoints := make(map[string][]KeyValuePoint)
	for _, point := range points {
		groupingKey := fmt.Sprintf(
			"%s,%s,%s,%s,%s,%s",
			point.Timestamp,
			point.Namespace,
			point.ResourceId,
			point.ResourceSubId,
			point.Dimensions,
			point.TimeGrain,
		)

		groupedPoints[groupingKey] = append(groupedPoints[groupingKey], point)
	}

	// Create an event for each group of points and send
	// it to Elasticsearch.
	for _, _points := range groupedPoints {
		if len(_points) == 0 {
			// This should never happen, but I don't feel like
			// writing points[0] without checking the length first.
			continue
		}

		// We assume that all points have the same timestamp and
		// dimensions because they were grouped by the same key.
		//
		// We use the reference point to get the resource ID and
		// all other information common to all points.
		referencePoint := _points[0]

		// Look up the full cloud resource information in the cache.
		resource := client.LookupResource(referencePoint.ResourceId)

		// Build the event using all the information we have.
		event, err := buildEventFrom(referencePoint, _points, resource, client.Config.DefaultResourceType)
		if err != nil {
			return err
		}

		//
		// Enrich the event with cloud metadata.
		//
		if client.Config.AddCloudMetadata {
			vm := client.GetVMForMetadata(&resource, referencePoint)
			addCloudVMMetadata(&event, vm, resource.Subscription)
		}

		//
		// Report the event to Elasticsearch.
		//
		reporter.Event(event)
	}

	return nil
}

// NewMockClient instantiates a new client with the mock azure service
func NewMockClient() *Client {
	azureMockService := new(MockService)
	logger := logp.NewLogger("test azure monitor")
	client := &Client{
		BaseClient: &BaseClient{
			AzureMonitorService: azureMockService,
			Config:              Config{},
			Log:                 logger,
			MetricRegistry:      NewMetricRegistry(logger),
		},
	}
	return client
}
