package controller

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	"github.com/nebuly-ai/nebulnetes/pkg/api/n8s.nebuly.ai/v1alpha1"
	"github.com/nebuly-ai/nebulnetes/pkg/constant"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	quota "k8s.io/apiserver/pkg/quota/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	kubefeatures "k8s.io/kubernetes/pkg/features"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"sort"
)

const (
	podPhaseKey = "status.phase"
)

// ElasticQuotaReconciler reconciles a ElasticQuota object
type ElasticQuotaReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=n8s.nebuly.ai,resources=elasticquotas,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=n8s.nebuly.ai,resources=elasticquotas/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=n8s.nebuly.ai,resources=elasticquotas/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;update;patch

func (r *ElasticQuotaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch EQ instance
	var instance v1alpha1.ElasticQuota
	if err := r.Client.Get(ctx, req.NamespacedName, &instance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Fetch running Pods in the EQ namespace
	var runningPodList v1.PodList
	opts := []client.ListOption{
		client.InNamespace(req.Namespace),
		client.MatchingFields{podPhaseKey: string(v1.PodRunning)},
	}
	if err := r.Client.List(ctx, &runningPodList, opts...); err != nil {
		logger.Error(err, "unable to list running Pods")
		return ctrl.Result{}, err
	}

	// Add quota status labels and compute used quota
	usedResourceList, err := r.patchPodsAndGetUsedQuota(ctx, &runningPodList, &instance)
	if err != nil {
		return ctrl.Result{}, err
	}
	instance.Status.Used = usedResourceList

	// Update EQ status
	err = r.updateStatus(ctx, &instance, logger)
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ElasticQuotaReconciler) patchPodsAndGetUsedQuota(ctx context.Context, podList *v1.PodList, eq *v1alpha1.ElasticQuota) (v1.ResourceList, error) {
	// Sort running Pods by creation timestamps
	sort.Slice(podList.Items, func(i, j int) bool {
		firstPodCT := podList.Items[i].ObjectMeta.CreationTimestamp
		secondPodCT := podList.Items[j].ObjectMeta.CreationTimestamp
		// If creation timestamp is the same, sort by Name alphabetically for deterministic results
		if firstPodCT.Equal(&secondPodCT) {
			return podList.Items[i].Name < podList.Items[j].Name
		}
		return firstPodCT.Before(&secondPodCT)
	})

	used := newZeroUsed(eq)
	var err error
	for _, pod := range podList.Items {
		request := computePodResourceRequest(&pod)
		used = quota.Add(used, request)

		var desiredCapacityInfo constant.CapacityInfo
		if less, _ := quota.LessThanOrEqual(used, eq.Spec.Min); less {
			desiredCapacityInfo = constant.CapacityInfoInQuota
		} else {
			desiredCapacityInfo = constant.CapacityInfoOverQuota
		}

		if _, err = r.patchCapacityInfoIfDifferent(ctx, &pod, desiredCapacityInfo); err != nil {
			return used, err
		}
	}

	return used, nil
}

func (r *ElasticQuotaReconciler) patchCapacityInfoIfDifferent(ctx context.Context, pod *v1.Pod, desired constant.CapacityInfo) (bool, error) {
	logger := log.FromContext(ctx)
	desiredAsString := string(desired)
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	if pod.Labels[constant.LabelCapacityInfo] != desiredAsString {
		logger.V(1).Info(
			"updating Pod capacity info label",
			"currentValue",
			pod.Labels[constant.LabelCapacityInfo],
			"desiredValue",
			desiredAsString,
			"Pod",
			pod,
		)
		original := pod.DeepCopy()
		pod.Labels[constant.LabelCapacityInfo] = desiredAsString
		if err := r.Client.Patch(ctx, pod, client.MergeFrom(original)); err != nil {
			msg := fmt.Sprintf("unable to update label %q with value %q", constant.LabelCapacityInfo, desiredAsString)
			logger.Error(err, msg, "pod", original)
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// computePodResourceRequest returns a v1.ResourceList that covers the largest
// width in each resource dimension. Because init-containers run sequentially, we collect
// the max in each dimension iteratively. In contrast, we sum the resource vectors for
// regular containers since they run simultaneously.
//
// If Pod Overhead is specified and the feature gate is set, the resources defined for Overhead
// are added to the calculated Resource request sum
//
// Example:
//
// Pod:
//
//	InitContainers
//	  IC1:
//	    CPU: 2
//	    Memory: 1G
//	  IC2:
//	    CPU: 2
//	    Memory: 3G
//	Containers
//	  C1:
//	    CPU: 2
//	    Memory: 1G
//	  C2:
//	    CPU: 1
//	    Memory: 1G
//
// Result: CPU: 3, Memory: 3G
func computePodResourceRequest(pod *v1.Pod) v1.ResourceList {
	result := v1.ResourceList{}
	for _, container := range pod.Spec.Containers {
		result = quota.Add(result, container.Resources.Requests)
	}
	initRes := v1.ResourceList{}
	// take max_resource for init_containers
	for _, container := range pod.Spec.InitContainers {
		initRes = quota.Max(initRes, container.Resources.Requests)
	}
	// If Overhead is being utilized, add to the total requests for the pod
	if pod.Spec.Overhead != nil && utilfeature.DefaultFeatureGate.Enabled(kubefeatures.PodOverhead) {
		quota.Add(result, pod.Spec.Overhead)
	}
	// take max_resource for init_containers and containers
	return quota.Max(result, initRes)
}

// newZeroUsed will return the zero value of the union of min and max
func newZeroUsed(eq *v1alpha1.ElasticQuota) v1.ResourceList {
	minResources := quota.ResourceNames(eq.Spec.Min)
	maxResources := quota.ResourceNames(eq.Spec.Max)
	res := v1.ResourceList{}
	for _, v := range minResources {
		res[v] = *resource.NewQuantity(0, resource.DecimalSI)
	}
	for _, v := range maxResources {
		res[v] = *resource.NewQuantity(0, resource.DecimalSI)
	}
	return res
}

func (r *ElasticQuotaReconciler) findElasticQuotaForPod(pod client.Object) []reconcile.Request {
	ctx := context.Background()
	logger := log.FromContext(ctx)

	var eqList v1alpha1.ElasticQuotaList
	err := r.Client.List(ctx, &eqList, client.InNamespace(pod.GetNamespace()))
	if err != nil {
		logger.Error(err, "unable to list ElasticQuotas")
		return []reconcile.Request{}
	}

	if len(eqList.Items) > 0 {
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Name:      eqList.Items[0].Name,
				Namespace: eqList.Items[0].Namespace,
			},
		}}
	}

	return []reconcile.Request{}
}

func (r *ElasticQuotaReconciler) updateStatus(ctx context.Context, instance *v1alpha1.ElasticQuota, logger logr.Logger) error {
	var currentEq v1alpha1.ElasticQuota
	namespacedName := types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}

	if err := r.Get(ctx, namespacedName, &currentEq); err != nil {
		logger.Error(err, "unable to fetch ElasticQuota")
		return err
	}
	if equality.Semantic.DeepEqual(currentEq.Status, instance.Status) {
		logger.V(1).Info("current status and desired status of ElasticQuota are equal, skipping update")
		return nil
	}
	logger.V(1).Info("updating ElasticQuota status", "Status", instance.Status)
	if err := r.Status().Update(ctx, instance); err != nil {
		logger.Error(err, "unable to update ElasticQuota status")
		return err
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ElasticQuotaReconciler) SetupWithManager(mgr ctrl.Manager, name string) error {
	err := mgr.GetFieldIndexer().IndexField(context.Background(), &v1.Pod{}, podPhaseKey, func(rawObj client.Object) []string {
		pod := rawObj.(*v1.Pod)
		return []string{string(pod.Status.Phase)}
	})
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ElasticQuota{}).
		Named(name).
		Watches(
			&source.Kind{Type: &v1.Pod{}},
			handler.EnqueueRequestsFromMapFunc(r.findElasticQuotaForPod),
			builder.WithPredicates(
				predicate.Funcs{
					CreateFunc: func(_ event.CreateEvent) bool {
						return false
					},
					DeleteFunc: func(_ event.DeleteEvent) bool {
						return true
					},
					UpdateFunc: func(updateEvent event.UpdateEvent) bool {
						// Reconcile only if Pod changed phase, and either old or new phase status is Running
						newPod := updateEvent.ObjectNew.(*v1.Pod)
						oldPod := updateEvent.ObjectOld.(*v1.Pod)
						statusChanged := newPod.Status.Phase != oldPod.Status.Phase
						anyRunning := (newPod.Status.Phase == v1.PodRunning) || (oldPod.Status.Phase == v1.PodRunning)
						return statusChanged && anyRunning
					},
					GenericFunc: func(_ event.GenericEvent) bool {
						return false
					},
				},
			),
		).
		Complete(r)
}