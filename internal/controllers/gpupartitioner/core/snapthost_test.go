/*
 * Copyright 2022 Nebuly.ai
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package core_test

import (
	"github.com/nebuly-ai/nebulnetes/internal/controllers/gpupartitioner/core"
	"github.com/nebuly-ai/nebulnetes/pkg/api/n8s.nebuly.ai/v1alpha1"
	"github.com/nebuly-ai/nebulnetes/pkg/constant"
	"github.com/nebuly-ai/nebulnetes/pkg/gpu"
	"github.com/nebuly-ai/nebulnetes/pkg/gpu/mig"
	"github.com/nebuly-ai/nebulnetes/pkg/test/factory"
	"github.com/nebuly-ai/nebulnetes/pkg/test/mocks"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"testing"
)

func newMigSnapshot(t *testing.T, nodes []v1.Node) core.Snapshot {
	migNodes := make(map[string]core.PartitionableNode, len(nodes))
	for _, n := range nodes {
		nodeInfo := framework.NewNodeInfo()
		nodeInfo.SetNode(&n)
		migNode, err := mig.NewNode(*nodeInfo)
		if err != nil {
			panic(err)
		}
		migNodes[n.Name] = &migNode
	}
	return core.NewClusterSnapshot(
		migNodes,
		mocks.NewPartitioner(t),
		mocks.NewSliceCalculator(t),
		mocks.NewSliceFilter(t),
	)
}

func TestSnapshot__Forking(t *testing.T) {
	t.Run("Forking multiple times shall return error", func(t *testing.T) {
		snapshot := newMigSnapshot(t, []v1.Node{})
		assert.NoError(t, snapshot.Fork())
		assert.Error(t, snapshot.Fork())
	})

	t.Run("Test Revert changes", func(t *testing.T) {
		node := factory.BuildNode("node-1").WithLabels(map[string]string{
			v1alpha1.LabelGpuPartitioning: gpu.PartitioningKindMig.String(),
			constant.LabelNvidiaProduct:   string(gpu.GPUModel_A100_PCIe_80GB),
			constant.LabelNvidiaCount:     "1",
		}).Get()
		snapshot := newMigSnapshot(t, []v1.Node{node})
		originalNodes := make(map[string]core.PartitionableNode)
		for k, v := range snapshot.GetNodes() {
			originalNodes[k] = v.Clone().(core.PartitionableNode)
		}
		pod := factory.BuildPod("ns-1", "pod-1").WithContainer(
			factory.BuildContainer("c1", "i1").WithCPUMilliRequest(1000).Get(),
		).Get()
		assert.NoError(t, snapshot.Fork())
		assert.NoError(t, snapshot.AddPod("node-1", pod))
		// Snapshot modified, should differ from original one
		for _, n := range originalNodes {
			snapshotNode, ok := snapshot.GetNode(n.GetName())
			assert.True(t, ok)
			snapshotRequested := snapshotNode.NodeInfo().Requested
			originalRequested := n.NodeInfo().Requested
			assert.NotEqual(t, originalRequested, snapshotRequested)
		}
		// Revert changes
		snapshot.Revert()
		// Changes reverted, snapshot should be equal as the original one before the changes
		for _, n := range originalNodes {
			snapshotNode, ok := snapshot.GetNode(n.GetName())
			assert.True(t, ok)
			snapshotRequested := snapshotNode.NodeInfo().Requested
			originalRequested := n.NodeInfo().Requested
			assert.Equal(t, originalRequested, snapshotRequested)
		}
	})

	t.Run("Test Commit changes", func(t *testing.T) {
		node := factory.BuildNode("node-1").WithLabels(map[string]string{
			v1alpha1.LabelGpuPartitioning: gpu.PartitioningKindMig.String(),
			constant.LabelNvidiaProduct:   string(gpu.GPUModel_A100_PCIe_80GB),
			constant.LabelNvidiaCount:     "1",
		}).Get()
		snapshot := newMigSnapshot(t, []v1.Node{node})
		originalNodes := make(map[string]core.PartitionableNode)
		for k, v := range snapshot.GetNodes() {
			originalNodes[k] = v.Clone().(core.PartitionableNode)
		}
		pod := factory.BuildPod("ns-1", "pod-1").WithContainer(
			factory.BuildContainer("c1", "i1").WithCPUMilliRequest(1000).Get(),
		).Get()
		assert.NoError(t, snapshot.Fork())
		assert.NoError(t, snapshot.AddPod("node-1", pod))
		// Snapshot modified, should differ from original one
		for _, n := range originalNodes {
			snapshotNode, ok := snapshot.GetNode(n.GetName())
			assert.True(t, ok)
			snapshotRequested := snapshotNode.NodeInfo().Requested
			originalRequested := n.NodeInfo().Requested
			assert.NotEqual(t, originalRequested, snapshotRequested)
		}
		// Commit changes
		snapshot.Commit()
		for _, n := range originalNodes {
			snapshotNode, ok := snapshot.GetNode(n.GetName())
			assert.True(t, ok)
			snapshotRequested := snapshotNode.NodeInfo().Requested
			originalRequested := n.NodeInfo().Requested
			assert.NotEqual(t, originalRequested, snapshotRequested)
		}
		// After committing it should be possible to fork the snapshot again
		assert.NoError(t, snapshot.Fork())
	})
}