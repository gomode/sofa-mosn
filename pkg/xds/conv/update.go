/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package conv

import (
	"fmt"

	"sofastack.io/sofa-mosn/pkg/api/v2"
	"sofastack.io/sofa-mosn/pkg/config"
	"sofastack.io/sofa-mosn/pkg/log"
	"sofastack.io/sofa-mosn/pkg/router"
	"sofastack.io/sofa-mosn/pkg/server"
	"sofastack.io/sofa-mosn/pkg/types"
	clusterAdapter "sofastack.io/sofa-mosn/pkg/upstream/cluster"
	envoy_api_v2 "github.com/envoyproxy/go-control-plane/envoy/api/v2"
	jsoniter "github.com/json-iterator/go"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

// ConvertXXX Function converts protobuf to mosn config, and makes the config effects

// ConvertAddOrUpdateRouters converts router configurationm, used to add or update routers
func ConvertAddOrUpdateRouters(routers []*envoy_api_v2.RouteConfiguration) {
	if routersMngIns := router.GetRoutersMangerInstance(); routersMngIns == nil {
		log.DefaultLogger.Errorf("xds OnAddOrUpdateRouters error: router manager in nil")
	} else {

		for _, router := range routers {
			if jsonStr, err := json.Marshal(router); err == nil {
				log.DefaultLogger.Tracef("raw router config: %s", string(jsonStr))
			}

			mosnRouter, _ := ConvertRouterConf("", router)
			log.DefaultLogger.Tracef("mosnRouter config: %+v", mosnRouter)
			routersMngIns.AddOrUpdateRouters(mosnRouter)
		}
	}
}

// ConvertAddOrUpdateListeners converts listener configuration, used to  add or update listeners
func ConvertAddOrUpdateListeners(listeners []*envoy_api_v2.Listener) {
	for _, listener := range listeners {
		if jsonStr, err := json.Marshal(listener); err == nil {
			log.DefaultLogger.Tracef("raw listener config: %s", string(jsonStr))
		}

		mosnListener := ConvertListenerConfig(listener)
		if mosnListener == nil {
			continue
		}

		var streamFilters []types.StreamFilterChainFactory
		var networkFilters []types.NetworkFilterChainFactory

		if !mosnListener.HandOffRestoredDestinationConnections {
			for _, filterChain := range mosnListener.FilterChains {
				nf := config.GetNetworkFilters(&filterChain)
				networkFilters = append(networkFilters, nf...)
			}
			streamFilters = config.GetStreamFilters(mosnListener.StreamFilters)

			if len(networkFilters) == 0 {
				log.DefaultLogger.Errorf("xds client update listener error: proxy needed in network filters")
				continue
			}
		}

		listenerAdapter := server.GetListenerAdapterInstance()
		if listenerAdapter == nil {
			// if listenerAdapter is nil, return directly
			log.DefaultLogger.Errorf("listenerAdapter is nil and hasn't been initiated at this time")
			return
		}
		log.DefaultLogger.Debugf("listenerAdapter.AddOrUpdateListener called, with mosn Listener:%+v, networkFilters:%+v, streamFilters: %+v",
			mosnListener, networkFilters, streamFilters)

		if err := listenerAdapter.AddOrUpdateListener("", mosnListener, networkFilters, streamFilters); err == nil {
			log.DefaultLogger.Debugf("xds AddOrUpdateListener success,listener address = %s", mosnListener.Addr.String())
		} else {
			log.DefaultLogger.Errorf("xds AddOrUpdateListener failure,listener address = %s, msg = %s ",
				mosnListener.Addr.String(), err.Error())
		}
	}

}

// ConvertDeleteListeners converts listener configuration, used to delete listener
func ConvertDeleteListeners(listeners []*envoy_api_v2.Listener) {
	for _, listener := range listeners {
		mosnListener := ConvertListenerConfig(listener)
		if mosnListener == nil {
			continue
		}

		listenerAdapter := server.GetListenerAdapterInstance()
		if listenerAdapter == nil {
			log.DefaultLogger.Errorf("listenerAdapter is nil and hasn't been initiated at this time")
			return
		}
		if err := listenerAdapter.DeleteListener("", mosnListener.Name); err == nil {
			log.DefaultLogger.Debugf("xds OnDeleteListeners success,listener address = %s", mosnListener.Addr.String())
		} else {
			log.DefaultLogger.Errorf("xds OnDeleteListeners failure,listener address = %s, mag = %s ",
				mosnListener.Addr.String(), err.Error())

		}
	}
}

// ConvertUpdateClusters converts cluster configuration, used to udpate cluster
func ConvertUpdateClusters(clusters []*envoy_api_v2.Cluster) {
	for _, cluster := range clusters {
		if jsonStr, err := json.Marshal(cluster); err == nil {
			log.DefaultLogger.Tracef("raw cluster config: %s", string(jsonStr))
		}
	}

	mosnClusters := ConvertClustersConfig(clusters)

	for _, cluster := range mosnClusters {
		var err error
		log.DefaultLogger.Debugf("update cluster: %+v\n", cluster)
		if cluster.ClusterType == v2.EDS_CLUSTER {
			err = clusterAdapter.GetClusterMngAdapterInstance().TriggerClusterAddOrUpdate(*cluster)
		} else {
			err = clusterAdapter.GetClusterMngAdapterInstance().TriggerClusterAndHostsAddOrUpdate(*cluster, cluster.Hosts)
		}

		if err != nil {
			log.DefaultLogger.Errorf("xds OnUpdateClusters failed,cluster name = %s, error: %v", cluster.Name, err.Error())

		} else {
			log.DefaultLogger.Debugf("xds OnUpdateClusters success,cluster name = %s", cluster.Name)
		}
	}

}

// ConvertDeleteClusters converts cluster configuration, used to delete cluster
func ConvertDeleteClusters(clusters []*envoy_api_v2.Cluster) {
	mosnClusters := ConvertClustersConfig(clusters)

	for _, cluster := range mosnClusters {
		log.DefaultLogger.Debugf("delete cluster: %+v\n", cluster)
		var err error
		if cluster.ClusterType == v2.EDS_CLUSTER {
			err = clusterAdapter.GetClusterMngAdapterInstance().TriggerClusterDel(cluster.Name)
		}

		if err != nil {
			log.DefaultLogger.Errorf("xds OnDeleteClusters failed,cluster name = %s, error: %v", cluster.Name, err.Error())

		} else {
			log.DefaultLogger.Debugf("xds OnDeleteClusters success,cluster name = %s", cluster.Name)
		}
	}
}

// ConverUpdateEndpoints converts cluster configuration, used to udpate hosts
func ConvertUpdateEndpoints(loadAssignments []*envoy_api_v2.ClusterLoadAssignment) error {
	var errGlobal error

	for _, loadAssignment := range loadAssignments {
		clusterName := loadAssignment.ClusterName

		for _, endpoints := range loadAssignment.Endpoints {
			hosts := ConvertEndpointsConfig(&endpoints)
			log.DefaultLogger.Debugf("xds client update endpoints: cluster: %s, priority: %d", loadAssignment.ClusterName, endpoints.Priority)
			for index, host := range hosts {
				log.DefaultLogger.Debugf("host[%d] is : %+v", index, host)
			}

			clusterMngAdapter := clusterAdapter.GetClusterMngAdapterInstance()
			if clusterMngAdapter == nil {
				log.DefaultLogger.Errorf("xds client update Error: clusterMngAdapter nil , hosts are %+v", hosts)
				errGlobal = fmt.Errorf("xds client update Error: clusterMngAdapter nil , hosts are %+v", hosts)
			}

			if err := clusterAdapter.GetClusterMngAdapterInstance().TriggerClusterHostUpdate(clusterName, hosts); err != nil {
				log.DefaultLogger.Errorf("xds client update Error = %s, hosts are %+v", err.Error(), hosts)
				errGlobal = fmt.Errorf("xds client update Error = %s, hosts are %+v", err.Error(), hosts)

			} else {
				log.DefaultLogger.Debugf("xds client update host success,hosts are %+v", hosts)
			}
		}
	}

	return errGlobal
}
