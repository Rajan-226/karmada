package core

import (
	"fmt"
	"sort"

	clusterv1alpha1 "github.com/karmada-io/karmada/pkg/apis/cluster/v1alpha1"
	policyv1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
	workv1alpha2 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha2"
	"github.com/karmada-io/karmada/pkg/scheduler/framework"
	"github.com/karmada-io/karmada/pkg/util"
	"github.com/karmada-io/karmada/pkg/util/helper"
)

// TargetClustersList is a slice of TargetCluster that implements sort.Interface to sort by Value.
type TargetClustersList []workv1alpha2.TargetCluster

func (a TargetClustersList) Len() int           { return len(a) }
func (a TargetClustersList) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a TargetClustersList) Less(i, j int) bool { return a[i].Replicas > a[j].Replicas }

func getStaticWeightInfoList(clusters []*clusterv1alpha1.Cluster, weightList []policyv1alpha1.StaticClusterWeight) helper.ClusterWeightInfoList {
	list := make(helper.ClusterWeightInfoList, 0)
	for _, cluster := range clusters {
		var weight int64
		for _, staticWeightRule := range weightList {
			if util.ClusterMatches(cluster, staticWeightRule.TargetCluster) {
				weight = util.MaxInt64(weight, staticWeightRule.Weight)
			}
		}
		if weight > 0 {
			list = append(list, helper.ClusterWeightInfo{
				ClusterName: cluster.Name,
				Weight:      weight,
			})
		}
	}
	if list.GetWeightSum() == 0 {
		for _, cluster := range clusters {
			list = append(list, helper.ClusterWeightInfo{
				ClusterName: cluster.Name,
				Weight:      1,
			})
		}
	}
	return list
}

// dynamicDivideReplicas assigns a total number of replicas to the selected clusters by preference according to the resource.
func dynamicDivideReplicas(state *assignState) ([]workv1alpha2.TargetCluster, error) {
	if state.availableReplicas < state.targetReplicas {
		return nil, &framework.UnschedulableError{Message: fmt.Sprintf("Clusters available replicas %d are not enough to schedule.", state.availableReplicas)}
	}

	switch state.strategyType {
	case AggregatedStrategy:
		state.availableClusters = state.resortAvailableClusters()
		var sum int32
		for i := range state.availableClusters {
			if sum += state.availableClusters[i].Replicas; sum >= state.targetReplicas {
				state.availableClusters = state.availableClusters[:i+1]
				break
			}
		}
		fallthrough
	case DynamicWeightStrategy:
		// Set the availableClusters as the weight, scheduledClusters as init result, target as the dispenser object.
		// After dispensing, the target cluster will be the combination of init result and weighted result for target replicas.
		return helper.SpreadReplicasByTargetClusters(state.targetReplicas, state.availableClusters, state.scheduledClusters), nil
	default:
		// should never happen
		return nil, fmt.Errorf("undefined strategy type: %s", state.strategyType)
	}
}

func dynamicScaleDown(state *assignState) ([]workv1alpha2.TargetCluster, error) {
	// The previous scheduling result will be the weight reference of scaling down.
	// In other words, we scale down the replicas proportionally by their scheduled replicas.
	// Now:
	// 1. targetReplicas is set to desired replicas.
	// 2. availableClusters is set to the former schedule result.
	// 3. scheduledClusters and assignedReplicas are not set, which implicates we consider this action as a first schedule.
	state.targetReplicas = state.spec.Replicas
	state.scheduledClusters = nil
	state.buildAvailableClusters(func(_ []*clusterv1alpha1.Cluster, spec *workv1alpha2.ResourceBindingSpec) []workv1alpha2.TargetCluster {
		availableClusters := make(TargetClustersList, len(spec.Clusters))
		copy(availableClusters, spec.Clusters)
		sort.Sort(availableClusters)
		return availableClusters
	})
	return dynamicDivideReplicas(state)
}

func dynamicScaleUp(state *assignState) ([]workv1alpha2.TargetCluster, error) {
	// Target is the extra ones.
	state.targetReplicas = state.spec.Replicas - state.assignedReplicas
	state.buildAvailableClusters(func(clusters []*clusterv1alpha1.Cluster, spec *workv1alpha2.ResourceBindingSpec) []workv1alpha2.TargetCluster {
		clusterAvailableReplicas := calAvailableReplicas(clusters, spec)
		sort.Sort(TargetClustersList(clusterAvailableReplicas))
		return clusterAvailableReplicas
	})
	return dynamicDivideReplicas(state)
}
