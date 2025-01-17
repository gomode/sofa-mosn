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

package metrics

import (
	"fmt"
	"reflect"
	"testing"
	gometrics "github.com/rcrowley/go-metrics"
	"sofastack.io/sofa-mosn/pkg/metrics/shm"
)

func TestGetAll(t *testing.T) {
	zone := shm.InitMetricsZone("TestGetAll", 10*1024)
	defer func() {
		zone.Detach()
		shm.Reset()
	}()

	ResetAll()

	// new some stats
	NewMetrics("type1", map[string]string{"lk": "lv"})
	NewMetrics("type2", map[string]string{"lk": "lv"})

	if len(GetAll()) != 2 {
		t.Errorf("get all lentgh error, expected 2, actual %d", len(GetAll()))
	}
}

func TestExclusionLabels(t *testing.T) {
	zone := shm.InitMetricsZone("TestExclusionLabels", 10*1024)
	defer func() {
		zone.Detach()
		shm.Reset()
	}()

	ResetAll()
	exclusions := []string{
		"exclusion",
	}
	SetStatsMatcher(false, exclusions, nil)
	// if a labels contains in exclusions, will returns a nil metrics
	// nil metrics works well.
	testCases := []struct {
		labels map[string]string
		isNil  bool
	}{
		{
			labels: map[string]string{
				"exclusion": "value",
			},
			isNil: true,
		},
		{
			labels: map[string]string{
				"lk":        "lv",
				"exclusion": "value",
			},
			isNil: true,
		},
		{
			labels: map[string]string{
				"lk": "exclusion",
			},
			isNil: false,
		},
		{
			labels: map[string]string{
				"lk": "lv",
			},
			isNil: false,
		},
	}
	for i, tc := range testCases {
		typ := fmt.Sprintf("test%d", i)
		m, _ := NewMetrics(typ, tc.labels)
		if _, ok := m.(*NilMetrics); ok != tc.isNil {
			t.Errorf("#%d create not expected", i)
		}
		if !(m.Type() == typ &&
			reflect.DeepEqual(tc.labels, m.Labels())) {
			t.Errorf("#%d type and labels is not expected", i)
		}
		// nil/non-nil metrics works well
		m.SortedLabels()
		m.Counter("conuter").Inc(1)
		m.Gauge("gauge").Update(1)
		m.Histogram("histogram").Update(1)
	}
	// Test reject all
	ResetAll()
	SetStatsMatcher(true, nil, nil)
	for i, tc := range testCases {
		typ := fmt.Sprintf("test%d", i)
		m, _ := NewMetrics(typ, tc.labels)
		if _, ok := m.(*NilMetrics); !ok {
			t.Errorf("#%d expected get nil metrics, but not")
		}
	}
}

func TestExclusionKeys(t *testing.T) {
	zone := shm.InitMetricsZone("TestExclusionKeys", 10*1024)
	defer func() {
		zone.Detach()
		shm.Reset()
	}()

	ResetAll()
	exclusions := []string{
		"exclusion",
	}
	SetStatsMatcher(false, nil, exclusions)
	// if a labels contains in exclusions, will returns a nil metrics
	// nil metrics works well.
	testCases := map[string]bool{
		"exclusion": true,
		"mk1":       false,
		"mk2":       false,
	}

	typ := "test"
	m, _ := NewMetrics(typ, map[string]string{"t": "t"})
	for key, isNil := range testCases {
		gauge := m.Gauge(key)
		if _, ok := gauge.(gometrics.NilGauge); ok != isNil {
			t.Errorf("%s create not expected", key)
		}
		// nil/non-nil metrics works well
		m.SortedLabels()
		gauge.Update(1)
	}
	// Test reject all
	ResetAll()
	SetStatsMatcher(true, nil, nil)
	for key, _ := range testCases {
		gauge := m.Gauge(key)
		if _, ok := gauge.(gometrics.NilGauge); !ok {
			t.Errorf("%s expected get nil in reject all scene, bot not", key)
		}
		// nil/non-nil metrics works well
		gauge.Update(1)
	}
}

func BenchmarkNewMetrics_SameLabels(b *testing.B) {
	ResetAll()
	total := b.N
	for i := 0; i < total; i++ {
		// should contains create map, same as different labels
		labels := map[string]string{
			"lk": "lv",
		}
		NewMetrics("typ", labels)
	}
	if len(GetAll()) != 1 {
		b.Error("same labels gets different metrics")
	}
}

func BenchmarkNewMetrics_DifferentLabels(b *testing.B) {
	ResetAll()
	total := b.N
	for i := 0; i < total; i++ {
		labels := map[string]string{
			"lk": fmt.Sprintf("lv%d", i),
		}
		NewMetrics("typ", labels)
	}
	registered := len(GetAll())
	if registered != total {
		b.Errorf("different labels gets same metrics, total %d, registered %d", total, registered)
	}
}
