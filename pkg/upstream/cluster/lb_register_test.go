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
	"testing"

	"sofastack.io/sofa-mosn/pkg/api/v2"
	"sofastack.io/sofa-mosn/pkg/protocol"
	"sofastack.io/sofa-mosn/pkg/types"
)

type headerLB struct {
	prioritySet types.PrioritySet // store the hosts
	key         string
	randLB      types.LoadBalancer
}

// header lb choose host from header's key, if not exists, random return one
func (lb *headerLB) ChooseHost(ctx types.LoadBalancerContext) types.Host {
	if headers := ctx.DownstreamHeaders(); headers != nil {
		if value, ok := headers.Get(lb.key); ok {
			hosts := lb.prioritySet.GetOrCreateHostSet(0).HealthyHosts()
			for _, h := range hosts {
				if h.Hostname() == value {
					return h
				}
			}
		}
	}
	// random choose a host
	return lb.randLB.ChooseHost(ctx)
}

type headerLBCfg struct {
	key string
}

func (cfg *headerLBCfg) newLB(ps types.PrioritySet) types.LoadBalancer {
	return &headerLB{
		prioritySet: ps,
		key:         cfg.key,
		randLB:      newRandomLoadbalancer(ps),
	}
}

const headerKey types.LoadBalancerType = "HeaderKey"

// Test Registered new load balancer
// subset load balancer is valid too.
func TestRegisterNewLB(t *testing.T) {
	cfg := &headerLBCfg{
		key: "hostname",
	}
	RegisterLBType(headerKey, cfg.newLB)
	// init hosts
	// reuse subset test config
	ps := createPrioritySet(ExampleHostConfigs())
	lb := NewLoadBalancer(headerKey, ps)
	// expected headerLB
	if _, ok := lb.(*headerLB); !ok {
		t.Fatal("load balancer created not expected")
	}
	ctx := newMockLbContextWithHeader(map[string]string{
		"version": "1.0",
	}, protocol.CommonHeader(map[string]string{
		"hostname": "e1",
	}))
	ctx2 := newMockLbContext(map[string]string{
		"version": "1.0",
	})
	// subset info is useless
	for i := 0; i < 100; i++ {
		host := lb.ChooseHost(ctx)
		if host == nil || host.Hostname() != "e1" {
			t.Fatal("choose host not expected, get: ", host)
		}
	}
	for i := 0; i < 100; i++ {
		host := lb.ChooseHost(ctx2)
		if host == nil {
			t.Fatal("choose host failed")
		}
	}

	// subset is also valid
	//  reuse subset test config
	subsetInfo := NewLBSubsetInfo(ExampleSubsetConfig())
	sublb := NewSubsetLoadBalancer(headerKey, ps, newClusterStats("test"), subsetInfo)
	// choose host is valid
	// 1. ctx contains subset matched config
	// 2. ctx contains header with key "hostname"
	// should choose e1 only
	for i := 0; i < 100; i++ {
		host := sublb.ChooseHost(ctx)
		if host == nil || host.Hostname() != "e1" {
			t.Fatal("choose host not expected, get: ", host)
		}
	}
	// choose e1,e2,e5
	for i := 0; i < 100; i++ {
		host := sublb.ChooseHost(ctx2)
		if host == nil {
			t.Fatal("choose host failed")
		}
		switch host.Hostname() {
		case "e1", "e2", "e5":
		default:
			t.Fatal("choose host not expected, get: ", host)
		}
	}
}

// Test Used in cluster
func TestNewLBCluster(t *testing.T) {
	cfg := v2.Cluster{
		Name:        "test",
		ClusterType: v2.SIMPLE_CLUSTER,
		LbType:      v2.LbType(headerKey), // same as lb type
	}
	c := newCluster(cfg, nil, true, nil)
	if c == nil || c.Info() == nil {
		t.Fatal("create cluster failed")
	}
	if c.Info().LbType() != headerKey {
		t.Fatal("create cluster lb type not expected")
	}
}
