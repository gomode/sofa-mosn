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

package cluster

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"sync"

	"time"

	admin "sofastack.io/sofa-mosn/pkg/admin/store"
	"sofastack.io/sofa-mosn/pkg/api/v2"
	"sofastack.io/sofa-mosn/pkg/log"
	"sofastack.io/sofa-mosn/pkg/network"
	"sofastack.io/sofa-mosn/pkg/rcu"
	"sofastack.io/sofa-mosn/pkg/types"
)

var (
	instanceMutex         = sync.Mutex{}
	clusterMangerInstance *clusterManager
)

const cycleTimes = 5

// ClusterManager
type clusterManager struct {
	sourceAddr             net.Addr
	primaryClusters        sync.Map // string: *primaryCluster
	protocolConnPool       sync.Map
	autoDiscovery          bool
	registryUseHealthCheck bool
	mux                    sync.Mutex
}

type clusterSnapshot struct {
	prioritySet  types.PrioritySet
	clusterInfo  types.ClusterInfo
	loadbalancer types.LoadBalancer
	value        *rcu.Value
	config       interface{}
}

func NewClusterManager(sourceAddr net.Addr, clusters []v2.Cluster,
	clusterMap map[string][]v2.Host, autoDiscovery bool, useHealthCheck bool) types.ClusterManager {
	instanceMutex.Lock()
	defer instanceMutex.Unlock()
	if clusterMangerInstance != nil {
		return clusterMangerInstance
	}

	clusterMangerInstance = &clusterManager{
		sourceAddr:       sourceAddr,
		primaryClusters:  sync.Map{},
		protocolConnPool: sync.Map{},
		autoDiscovery:    true, //todo delete
	}

	for k := range types.ConnPoolFactories {
		clusterMangerInstance.protocolConnPool.Store(k, &sync.Map{})
	}

	//init clusterMngInstance when run app
	initClusterMngAdapterInstance(clusterMangerInstance)

	//Add cluster to cm
	//Register upstream update type
	for _, cluster := range clusters {

		if !clusterMangerInstance.AddOrUpdatePrimaryCluster(cluster) {
			log.DefaultLogger.Errorf("[upstream] [cluster manager] NewClusterManager: AddOrUpdatePrimaryCluster failure, cluster name = %s", cluster.Name)
		}
	}

	// Add hosts to cluster
	// Note: currently, use priority = 0
	for clusterName, hosts := range clusterMap {
		clusterMangerInstance.UpdateClusterHosts(clusterName, 0, hosts)
	}

	return clusterMangerInstance
}

func (cs *clusterSnapshot) PrioritySet() types.PrioritySet {
	return cs.prioritySet
}

func (cs *clusterSnapshot) ClusterInfo() types.ClusterInfo {
	return cs.clusterInfo
}

func (cs *clusterSnapshot) LoadBalancer() types.LoadBalancer {
	return cs.loadbalancer
}

func (cs *clusterSnapshot) IsExistsHosts(metadata types.MetadataMatchCriteria) bool {
	if metadata == nil {
		for _, hostSet := range cs.PrioritySet().HostSetsByPriority() {
			if len(hostSet.Hosts()) > 0 {
				return true
			}
		}

		return false
	}

	if subsetLB, ok := cs.loadbalancer.(*subSetLoadBalancer); ok {
		return subsetLB.GetHostsNumber(metadata) > 0
	}

	log.DefaultLogger.Errorf("[upstream] [cluster snapshot] Call IsExistsHosts error,metadata isn't nil, but subsetLB doesn't exist")
	return false
}

type primaryCluster struct {
	cluster     types.Cluster
	addedViaAPI bool
	configUsed  *v2.Cluster // used for update
	configLock  *rcu.Value
	updateLock  sync.Mutex
}

func NewPrimaryCluster(cluster types.Cluster, config *v2.Cluster, addedViaAPI bool) *primaryCluster {
	return &primaryCluster{
		cluster:     cluster,
		addedViaAPI: addedViaAPI,
		configUsed:  config,
		updateLock:  sync.Mutex{},
		configLock:  rcu.NewValue(config),
	}
}
func (pc *primaryCluster) UpdateCluster(cluster types.Cluster, config *v2.Cluster, addedViaAPI bool) error {
	if cluster == nil || config == nil {
		return errors.New("cannot update nil cluster or cluster config")
	}
	pc.updateLock.Lock()
	defer pc.updateLock.Unlock()
	pc.cluster = cluster
	pc.configUsed = deepCopyCluster(config)
	pc.addedViaAPI = addedViaAPI
	if err := pc.configLock.Update(pc.configUsed, 0); err == rcu.Block {
		return err
	}
	return nil
}
func (pc *primaryCluster) UpdateHosts(hosts []types.Host) error {
	pc.updateLock.Lock()
	defer pc.updateLock.Unlock()
	if c, ok := pc.cluster.(*simpleInMemCluster); ok {
		c.UpdateHosts(hosts)
		hosts = c.hosts // set the final host
	}
	config := deepCopyCluster(pc.configUsed)
	hostsConfig := make([]v2.Host, 0, len(hosts))
	for _, h := range hosts {
		hostsConfig = append(hostsConfig, h.Config())
	}
	config.Hosts = hostsConfig
	pc.configUsed = config
	if err := pc.configLock.Update(pc.configUsed, 0); err == rcu.Block {
		return err
	}
	admin.SetHosts(pc.cluster.Info().Name(), hostsConfig)
	log.DefaultLogger.Infof("[cluster] [primaryCluster] [UpdateHosts] cluster %s update hosts: %v", pc.cluster.Info().Name(), hosts)
	return nil
}

func deepCopyCluster(cluster *v2.Cluster) *v2.Cluster {
	if cluster == nil {
		return nil
	}
	clusterCopy := *cluster
	return &clusterCopy
}

// AddOrUpdatePrimaryCluster
// used to "add" cluster if cluster not exist
// or "update" cluster when new cluster config if cluster already exist
func (cm *clusterManager) AddOrUpdatePrimaryCluster(cluster v2.Cluster) bool {
	clusterName := cluster.Name

	ok := false
	if v, exist := cm.primaryClusters.Load(clusterName); exist {
		if !v.(*primaryCluster).addedViaAPI {
			return false
		}
		// update cluster
		ok = cm.updateCluster(cluster, v.(*primaryCluster), true)
	} else {
		// add new cluster
		ok = cm.loadCluster(cluster, true)
	}
	if ok {
		admin.SetClusterConfig(clusterName, cluster)
		log.DefaultLogger.Infof("[cluster] [cluster manager] [AddOrUpdatePrimaryCluster] cluster %s updated", clusterName)
	}
	return ok
}

// AddClusterHealthCheckCallbacks add a callback for clustrer
func (cm *clusterManager) AddClusterHealthCheckCallbacks(name string, cb types.HealthCheckCb) bool {
	if v, ok := cm.primaryClusters.Load(name); ok {
		if cluster, ok := v.(*primaryCluster); ok {
			cluster.cluster.AddHealthCheckCallbacks(cb)
			return true
		}
	}
	return false
}

func (cm *clusterManager) ClusterExist(clusterName string) bool {
	if _, exist := cm.primaryClusters.Load(clusterName); exist {
		return true
	}

	return false
}

func (cm *clusterManager) updateCluster(clusterConf v2.Cluster, pcluster *primaryCluster, addedViaAPI bool) bool {
	// diff cluster
	if reflect.DeepEqual(clusterConf, *(pcluster.configUsed)) {
		log.DefaultLogger.Debugf("[upstream] [cluster manager] update cluster but get duplicate configure")
		return true
	}

	if concretedCluster, ok := pcluster.cluster.(*simpleInMemCluster); ok {
		hosts := concretedCluster.hosts
		cluster := NewCluster(clusterConf, cm.sourceAddr, addedViaAPI)
		cluster.(*simpleInMemCluster).UpdateHosts(hosts)
		pcluster.UpdateCluster(cluster, &clusterConf, addedViaAPI)
		return true
	}

	return false
}

func (cm *clusterManager) loadCluster(clusterConfig v2.Cluster, addedViaAPI bool) bool {
	//clusterConfig.UseHealthCheck
	cluster := NewCluster(clusterConfig, cm.sourceAddr, addedViaAPI)

	if nil == cluster {
		return false
	}

	cluster.Initialize(func() {
		cluster.PrioritySet().AddMemberUpdateCb(func(priority uint32, hostsAdded []types.Host, hostsRemoved []types.Host) {
		})
	})

	cm.primaryClusters.Store(clusterConfig.Name, NewPrimaryCluster(cluster, &clusterConfig, addedViaAPI))

	return true
}

func (cm *clusterManager) PutClusterSnapshot(snapshot types.ClusterSnapshot) {
	if snapshot == nil {
		return
	}
	if s, ok := snapshot.(*clusterSnapshot); ok {
		s.value.Put(s.config)
	} else {
		log.DefaultLogger.Errorf("[upstream] [cluster manager] snapshot is not clusterSnapshot, clustername=%s", snapshot.ClusterInfo().Name())
	}

}

func (cm *clusterManager) GetClusterSnapshot(context context.Context, clusterName string) types.ClusterSnapshot {
	if v, ok := cm.primaryClusters.Load(clusterName); ok {
		pc := v.(*primaryCluster)
		pcc := pc.cluster

		clusterSnapshot := &clusterSnapshot{
			prioritySet:  pcc.PrioritySet(),
			clusterInfo:  pcc.Info(),
			loadbalancer: pcc.Info().LBInstance(),
			value:        pc.configLock,
			config:       pc.configLock.Load(),
		}

		return clusterSnapshot
	}

	return nil
}

func (cm *clusterManager) RemovePrimaryCluster(clusterNames ...string) error {
	for _, clusterName := range clusterNames {
		if v, exist := cm.primaryClusters.Load(clusterName); exist {
			if !v.(*primaryCluster).addedViaAPI {
				return fmt.Errorf("Remove Primary Cluster Failed, Cluster Name = %s not addedViaAPI", clusterName)
			}
			cm.primaryClusters.Delete(clusterName)
			admin.RemoveClusterConfig(clusterName)
			if log.DefaultLogger.GetLogLevel() >= log.INFO {
				log.DefaultLogger.Infof("[upstream] [cluster manager] Remove Primary Cluster, Cluster Name = %s", clusterName)
			}
		} else {
			return fmt.Errorf("Remove Primary Cluster failure, cluster name = %s doesn't exist", clusterName)
		}
	}
	return nil
}

func (cm *clusterManager) SetInitializedCb(cb func()) {}

func (cm *clusterManager) UpdateClusterHosts(clusterName string, priority uint32, hostConfigs []v2.Host) error {
	if v, ok := cm.primaryClusters.Load(clusterName); ok {
		pc := v.(*primaryCluster)
		var hosts []types.Host
		for _, hc := range hostConfigs {
			hosts = append(hosts, NewHost(hc, pc.cluster.Info()))
		}
		if err := pc.UpdateHosts(hosts); err != nil {
			return fmt.Errorf("UpdateClusterHosts failed, cluster's hostset %s can't be update: %v", clusterName, err)
		}
		if log.DefaultLogger.GetLogLevel() >= log.INFO {
			log.DefaultLogger.Infof("[upstream] [cluster manager] update cluster %s hosts", clusterName)
		}
		return nil
	}

	return fmt.Errorf("UpdateClusterHosts failed, cluster %s not found", clusterName)
}

func (cm *clusterManager) AppendClusterHosts(clusterName string, priority uint32, hostConfigs []v2.Host) error {
	if v, ok := cm.primaryClusters.Load(clusterName); ok {
		pc := v.(*primaryCluster)
		pcc := pc.cluster
		var hosts []types.Host
		if concretedCluster, ok := pcc.(*simpleInMemCluster); ok {
			hosts = append(hosts, concretedCluster.hosts...)
		}
		for _, hc := range hostConfigs {
			hosts = append(hosts, NewHost(hc, pc.cluster.Info()))
		}
		if err := pc.UpdateHosts(hosts); err != nil {
			return fmt.Errorf("AppendClusterHosts failed, cluster's hostset %s can't be update: %v", clusterName, err)
		}
		if log.DefaultLogger.GetLogLevel() >= log.INFO {
			log.DefaultLogger.Infof("[upstream] [cluster manager] append hosts into cluster %s", clusterName)
		}
		return nil
	}
	return fmt.Errorf("AppendClusterHosts failed, cluster %s not found", clusterName)
}

func (cm *clusterManager) RemoveClusterHost(clusterName string, hostAddress string) error {
	if hostAddress == "" {
		return fmt.Errorf("RemoveClusterHost failed, hostAddress is nil")
	}

	if v, ok := cm.primaryClusters.Load(clusterName); ok {
		pc := v.(*primaryCluster)
		pcc := pc.cluster

		found := false
		if concretedCluster, ok := pcc.(*simpleInMemCluster); ok {
			var ccHosts []types.Host
			for i := 0; i < len(concretedCluster.hosts); i++ {
				if hostAddress == concretedCluster.hosts[i].AddressString() {
					ccHosts = append(ccHosts, concretedCluster.hosts[:i]...)
					ccHosts = append(ccHosts, concretedCluster.hosts[i+1:]...)
					found = true
					break
				}
			}
			if found == true {
				if err := pc.UpdateHosts(ccHosts); err != nil {
					return fmt.Errorf("remove host %s from cluster %s failed: %v", hostAddress, clusterName, err)
				}
				if log.DefaultLogger.GetLogLevel() >= log.INFO {
					log.DefaultLogger.Infof("[upstream] [cluster manager] RemoveClusterHost success, host address = %s", hostAddress)
				}
				return nil
			}
			return fmt.Errorf("RemoveClusterHost failed, host address = %s doesn't exist", hostAddress)

		}

		return fmt.Errorf("RemoveClusterHost failed, cluster name = %s is not valid", clusterName)
	}

	return fmt.Errorf("RemoveClusterHost failed, cluster name = %s doesn't exist", clusterName)
}

func (cm *clusterManager) TCPConnForCluster(lbCtx types.LoadBalancerContext, snapshot types.ClusterSnapshot) types.CreateConnectionData {
	if snapshot == nil {
		return types.CreateConnectionData{}
	}
	clusterSnapshot, ok := snapshot.(*clusterSnapshot)
	if !ok {
		return types.CreateConnectionData{}
	}

	host := clusterSnapshot.loadbalancer.ChooseHost(lbCtx)

	if host != nil {
		return host.CreateConnection(nil)
	}

	return types.CreateConnectionData{}
}

func (cm *clusterManager) ConnPoolForCluster(balancerContext types.LoadBalancerContext, snapshot types.ClusterSnapshot, protocol types.Protocol) types.ConnectionPool {
	if snapshot == nil {
		log.DefaultLogger.Errorf("[upstream] [cluster manager]  %s ConnPool For Cluster is nil, cluster name = %s", protocol, snapshot.ClusterInfo().Name())
		return nil
	}
	clusterSnapshot, ok := snapshot.(*clusterSnapshot)
	if !ok {
		log.DefaultLogger.Errorf("[upstream] [cluster manager] unexpected cluster snapshot")
		return nil
	}

	pool, err := cm.getActiveConnectionPool(balancerContext, clusterSnapshot, protocol)
	if err != nil {
		log.DefaultLogger.Errorf("[upstream] [cluster manager] ConnPoolForCluster Failed; %v", err)
	}

	return pool
}

func (cm *clusterManager) getActiveConnectionPool(balancerContext types.LoadBalancerContext, clusterSnapshot *clusterSnapshot, protocol types.Protocol) (types.ConnectionPool, error) {
	var pool types.ConnectionPool
	var pools [cycleTimes]types.ConnectionPool

	for i := 0; i < cycleTimes; i++ {
		host := clusterSnapshot.loadbalancer.ChooseHost(balancerContext)
		if host == nil {
			return nil, fmt.Errorf("clusterSnapshot.loadbalancer.ChooseHost is nil")
		}

		addr := host.AddressString()
		if log.DefaultLogger.GetLogLevel() >= log.DEBUG {
			log.DefaultLogger.Debugf("[upstream] [cluster manager] clusterSnapshot.loadbalancer.ChooseHost result is %s, cluster name = %s", addr, clusterSnapshot.clusterInfo.Name())
		}
		value, _ := cm.protocolConnPool.Load(protocol)

		connectionPool := value.(*sync.Map)
		if connPool, ok := connectionPool.Load(addr); ok {
			pool = connPool.(types.ConnectionPool)
			if pool.CheckAndInit(balancerContext.DownstreamContext()) {
				return pool, nil
			}
			pools[i] = pool
			if log.DefaultLogger.GetLogLevel() >= log.DEBUG {
				log.DefaultLogger.Debugf("[upstream] [cluster manager] cluster host %s is not active", addr)
			}

		} else {
			err := func() error {
				cm.mux.Lock()
				defer cm.mux.Unlock()

				if _, ok := connectionPool.Load(addr); !ok {
					if factory, ok := network.ConnNewPoolFactories[protocol]; ok {
						newPool := factory(host) //call NewBasicRoute
						connectionPool.Store(addr, newPool)
						newPool.CheckAndInit(balancerContext.DownstreamContext())
						pools[i] = newPool
					} else {
						return fmt.Errorf("NewPoolFactory is nil, protocol is %v", protocol)
					}
				}

				return nil
			}()

			if err != nil {
				return nil, err
			}
		}
	}

	// perhaps the first request, wait for tcp handshaking. total wait time: 1ms + 10ms + 100ms + 1000ms
	waitTime := time.Millisecond
	for t := 0; t < 4; t++ {
		time.Sleep(waitTime)
		for i := 0; i < cycleTimes; i++ {
			if pools[i] == nil {
				continue
			}
			if pools[i].CheckAndInit(balancerContext.DownstreamContext()) {
				return pools[i], nil
			}
		}
		waitTime *= 10
	}

	return nil, errors.New("no health hosts")
}

func (cm *clusterManager) Shutdown() error {
	return nil
}

func (cm *clusterManager) SourceAddress() net.Addr {
	return cm.sourceAddr
}

func (cm *clusterManager) VersionInfo() string {
	return ""
}

func (cm *clusterManager) LocalClusterName() string {
	return ""
}

// Destory the cluster manager instance
func (cm *clusterManager) Destory() {
	instanceMutex.Lock()
	defer instanceMutex.Unlock()
	if clusterMangerInstance != nil {
		clusterMangerInstance = nil
	}
}
