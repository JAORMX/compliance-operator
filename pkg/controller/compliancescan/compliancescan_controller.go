package compliancescan

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"sigs.k8s.io/controller-runtime/pkg/source"

	complianceoperatorv1alpha1 "github.com/openshift/compliance-operator/pkg/apis/complianceoperator/v1alpha1"
)

var log = logf.Log.WithName("controller_compliancescan")

var oneReplica int32 = 1

var (
	trueVal     = true
	hostPathDir = corev1.HostPathDirectory
)

const (
	// OpenSCAPScanContainerName defines the name of the contianer that will run OpenSCAP
	OpenSCAPScanContainerName = "openscap-ocp"
	OpenSCAPNodePodLabel      = "node-scan/"
	NodeHostnameLabel         = "kubernetes.io/hostname"
	AggregatorPodAnnotation   = "scan-aggregator"
	// The default time we should wait before requeuing
	requeueAfterDefault = 10 * time.Second
)

// Add creates a new ComplianceScan Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileComplianceScan{client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("compliancescan-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource ComplianceScan
	err = c.Watch(&source.Kind{Type: &complianceoperatorv1alpha1.ComplianceScan{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// TODO(user): Modify this to be the types you create that are owned by the primary resource
	// Watch for changes to secondary resource Pods and requeue the owner ComplianceScan
	err = c.Watch(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &complianceoperatorv1alpha1.ComplianceScan{},
	})
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcileComplianceScan implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileComplianceScan{}

// ReconcileComplianceScan reconciles a ComplianceScan object
type ReconcileComplianceScan struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme
}

// Reconcile reads that state of the cluster for a ComplianceScan object and makes changes based on the state read
// and what is in the ComplianceScan.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileComplianceScan) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling ComplianceScan")

	// Fetch the ComplianceScan instance
	instance := &complianceoperatorv1alpha1.ComplianceScan{}
	err := r.client.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	// At this point, we make a copy of the instance, so we can modify it in the functions below.
	scanToBeUpdated := instance.DeepCopy()

	// If no phase set, default to pending (the initial phase):
	if scanToBeUpdated.Status.Phase == "" {
		scanToBeUpdated.Status.Phase = complianceoperatorv1alpha1.PhasePending
	}

	switch scanToBeUpdated.Status.Phase {
	case complianceoperatorv1alpha1.PhasePending:
		return r.phasePendingHandler(scanToBeUpdated, reqLogger)
	case complianceoperatorv1alpha1.PhaseLaunching:
		return r.phaseLaunchingHandler(scanToBeUpdated, reqLogger)
	case complianceoperatorv1alpha1.PhaseRunning:
		return r.phaseRunningHandler(scanToBeUpdated, reqLogger)
	case complianceoperatorv1alpha1.PhaseAggregating:
		return r.phaseAggregatingHandler(scanToBeUpdated, reqLogger)
	case complianceoperatorv1alpha1.PhaseDone:
		return r.phaseDoneHandler(scanToBeUpdated, reqLogger)
	}

	// the default catch-all, just remove the request from the queue
	return reconcile.Result{}, nil
}

func (r *ReconcileComplianceScan) phasePendingHandler(instance *complianceoperatorv1alpha1.ComplianceScan, logger logr.Logger) (reconcile.Result, error) {
	logger.Info("Phase: Pending", "ComplianceScan", instance.ObjectMeta.Name)

	err := createConfigMaps(r, scriptCmForScan(instance), envCmForScan(instance), instance)
	if err != nil {
		logger.Error(err, "Cannot create the configmaps")
		return reconcile.Result{}, err
	}

	// Update the scan instance, the next phase is running
	instance.Status.Phase = complianceoperatorv1alpha1.PhaseLaunching
	err = r.client.Status().Update(context.TODO(), instance)
	if err != nil {
		return reconcile.Result{}, err
	}

	// TODO: It might be better to store the list of eligible nodes in the CR so that if someone edits the CR or
	// adds/removes nodes while the scan is running, we just work on the same set?

	return reconcile.Result{}, nil
}

func (r *ReconcileComplianceScan) phaseLaunchingHandler(instance *complianceoperatorv1alpha1.ComplianceScan, logger logr.Logger) (reconcile.Result, error) {
	var nodes corev1.NodeList
	var err error

	logger.Info("Phase: Launching", "ComplianceScan", instance.ObjectMeta.Name)

	if nodes, err = getTargetNodes(r, instance); err != nil {
		log.Error(err, "Cannot get nodes")
		return reconcile.Result{}, err
	}

	if err = r.handleRootCASecret(instance, logger); err != nil {
		log.Error(err, "Cannot create CA secret")
		return reconcile.Result{}, err
	}

	if err = r.handleResultServerSecret(instance, logger); err != nil {
		log.Error(err, "Cannot create result server cert secret")
		return reconcile.Result{}, err
	}

	if err = r.handleResultClientSecret(instance, logger); err != nil {
		log.Error(err, "Cannot create result client cert secret")
		return reconcile.Result{}, err
	}

	if err = r.createResultServer(instance, logger); err != nil {
		log.Error(err, "Cannot create result server")
		return reconcile.Result{}, err
	}

	if err = r.createScanPods(instance, nodes, logger); err != nil {
		return reconcile.Result{}, err
	}

	// if we got here, there are no new pods to be created, move to the next phase
	instance.Status.Phase = complianceoperatorv1alpha1.PhaseRunning
	err = r.client.Status().Update(context.TODO(), instance)
	if err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func (r *ReconcileComplianceScan) phaseRunningHandler(instance *complianceoperatorv1alpha1.ComplianceScan, logger logr.Logger) (reconcile.Result, error) {
	var nodes corev1.NodeList
	var err error

	logger.Info("Phase: Running", "ComplianceScan scan", instance.ObjectMeta.Name)

	if nodes, err = getTargetNodes(r, instance); err != nil {
		log.Error(err, "Cannot get nodes")
		return reconcile.Result{}, err
	}

	// On each eligible node..
	for _, node := range nodes.Items {
		running, err := isPodRunningInNode(r, instance, &node, logger)
		if errors.IsNotFound(err) {
			// Let's go back to the previous state and make sure all the nodes are covered.
			logger.Info("Phase: Running: A pod is missing. Going to state LAUNCHING to make sure we launch it",
				"compliancescan", instance.ObjectMeta.Name, "node", node.Name)
			instance.Status.Phase = complianceoperatorv1alpha1.PhaseLaunching
			err = r.client.Status().Update(context.TODO(), instance)
			if err != nil {
				return reconcile.Result{}, err
			}
			return reconcile.Result{}, nil
		} else if err != nil {
			return reconcile.Result{}, err
		}

		if running {
			// at least one pod is still running, just go back to the queue
			return reconcile.Result{}, err
		}
	}

	// if we got here, there are no pods running, move to the Aggregating phase
	instance.Status.Phase = complianceoperatorv1alpha1.PhaseAggregating
	err = r.client.Status().Update(context.TODO(), instance)
	if err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func (r *ReconcileComplianceScan) phaseAggregatingHandler(instance *complianceoperatorv1alpha1.ComplianceScan, logger logr.Logger) (reconcile.Result, error) {
	logger.Info("Phase: Aggregating", "ComplianceScan scan", instance.ObjectMeta.Name)

	var nodes corev1.NodeList
	var err error

	if nodes, err = getTargetNodes(r, instance); err != nil {
		log.Error(err, "Cannot get nodes")
		return reconcile.Result{}, err
	}

	result, isReady, err := gatherResults(r, instance, nodes)

	// We only wait if there are no errors.
	if err == nil && !isReady {
		log.Info("ConfigMap missing (not ready). Requeuing.")
		return reconcile.Result{Requeue: true, RequeueAfter: requeueAfterDefault}, nil
	}

	instance.Status.Result = result
	if err != nil {
		instance.Status.ErrorMessage = err.Error()
	}

	logger.Info("Creating an aggregator pod for scan", "scan", instance.Name)
	aggregator := newAggregatorPod(instance, logger)
	if err = controllerutil.SetControllerReference(instance, aggregator, r.scheme); err != nil {
		log.Error(err, "Failed to set aggregator pod ownership", "aggregator", aggregator)
		return reconcile.Result{}, err
	}

	err = r.launchAggregatorPod(instance, aggregator, logger)
	if err != nil {
		log.Error(err, "Failed to launch aggregator pod", "aggregator", aggregator)
		return reconcile.Result{}, err
	}

	running, err := isAggregatorRunning(r, instance, logger)
	if err != nil {
		log.Error(err, "Failed to check if aggregator pod is running", "aggregator", aggregator)
		return reconcile.Result{}, err
	}

	if running {
		log.Info("Remaining in the aggregating phase")
		instance.Status.Phase = complianceoperatorv1alpha1.PhaseAggregating
		err = r.client.Status().Update(context.TODO(), instance)
		return reconcile.Result{Requeue: true, RequeueAfter: requeueAfterDefault}, nil
	}

	log.Info("Moving on to the Done phase")
	instance.Status.Phase = complianceoperatorv1alpha1.PhaseDone
	err = r.client.Status().Update(context.TODO(), instance)
	return reconcile.Result{}, err
}

func (r *ReconcileComplianceScan) phaseDoneHandler(instance *complianceoperatorv1alpha1.ComplianceScan, logger logr.Logger) (reconcile.Result, error) {
	var nodes corev1.NodeList
	var err error
	logger.Info("Phase: Done", "ComplianceScan scan", instance.ObjectMeta.Name)
	if !instance.Spec.Debug {
		if nodes, err = getTargetNodes(r, instance); err != nil {
			log.Error(err, "Cannot get nodes")
			return reconcile.Result{}, err
		}

		if err := r.deleteScanPods(instance, nodes, logger); err != nil {
			log.Error(err, "Cannot delete scan pods")
			return reconcile.Result{}, err
		}

		if err := r.deleteResultServer(instance, logger); err != nil {
			log.Error(err, "Cannot delete result server")
			return reconcile.Result{}, err
		}

		if err := r.deleteAggregator(instance, logger); err != nil {
			log.Error(err, "Cannot delete aggregator")
			return reconcile.Result{}, err
		}

		if err = r.deleteResultServerSecret(instance, logger); err != nil {
			log.Error(err, "Cannot delete result server cert secret")
			return reconcile.Result{}, err
		}

		if err = r.deleteResultClientSecret(instance, logger); err != nil {
			log.Error(err, "Cannot delete result client cert secret")
			return reconcile.Result{}, err
		}

		if err = r.deleteRootCASecret(instance, logger); err != nil {
			log.Error(err, "Cannot delete CA secret")
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}

func getTargetNodes(r *ReconcileComplianceScan, instance *complianceoperatorv1alpha1.ComplianceScan) (corev1.NodeList, error) {
	var nodes corev1.NodeList

	listOpts := client.ListOptions{
		LabelSelector: labels.SelectorFromSet(instance.Spec.NodeSelector),
	}

	if err := r.client.List(context.TODO(), &nodes, &listOpts); err != nil {
		return nodes, err
	}

	return nodes, nil
}

func (r *ReconcileComplianceScan) createPVCForScan(instance *complianceoperatorv1alpha1.ComplianceScan) error {
	pvc := getPVCForScan(instance)
	if err := controllerutil.SetControllerReference(instance, pvc, r.scheme); err != nil {
		log.Error(err, "Failed to set pvc ownership", "pvc", pvc.Name)
		return err
	}
	if err := r.client.Create(context.TODO(), pvc); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// returns true if the pod is still running, false otherwise
func isPodRunningInNode(r *ReconcileComplianceScan, scanInstance *complianceoperatorv1alpha1.ComplianceScan, node *corev1.Node, logger logr.Logger) (bool, error) {
	logger.Info("Retrieving a pod for node", "node", node.Name)

	podName := getPodForNodeName(scanInstance, node.Name)
	return isPodRunning(r, podName, scanInstance.Namespace, logger)
}

func isPodRunning(r *ReconcileComplianceScan, podName, namespace string, logger logr.Logger) (bool, error) {
	foundPod := &corev1.Pod{}
	err := r.client.Get(context.TODO(), types.NamespacedName{Name: podName, Namespace: namespace}, foundPod)
	if err != nil {
		logger.Error(err, "Cannot retrieve pod", "pod", podName)
		return false, err
	} else if foundPod.Status.Phase == corev1.PodFailed || foundPod.Status.Phase == corev1.PodSucceeded {
		logger.Info("Pod has finished")
		return false, nil
	}

	// the pod is still running or being created etc
	logger.Info("Pod still running", "pod", podName)
	return true, nil
}

// gatherResults will iterate the nodes in the scan and get the results
// for the OpenSCAP check. If the results haven't yet been persisted in
// the relevant ConfigMap, the a requeue will be requested since the
// results are not ready.
func gatherResults(r *ReconcileComplianceScan, instance *complianceoperatorv1alpha1.ComplianceScan, nodes corev1.NodeList) (complianceoperatorv1alpha1.ComplianceScanStatusResult, bool, error) {
	var lastNonCompliance complianceoperatorv1alpha1.ComplianceScanStatusResult
	var result complianceoperatorv1alpha1.ComplianceScanStatusResult
	compliant := true
	isReady := true
	for _, node := range nodes.Items {
		targetCM := types.NamespacedName{
			Name:      getConfigMapForNodeName(instance.Name, node.Name),
			Namespace: instance.Namespace,
		}

		foundCM := &corev1.ConfigMap{}
		err := r.client.Get(context.TODO(), targetCM, foundCM)

		// Could be a transcient error, so we requeue if there's any
		// error here.
		if err != nil {
			isReady = false
		}

		// NOTE: err is only set if there is an error in the scan run
		result, err = getScanResult(foundCM)

		// we output the last result if it was an error
		if result == complianceoperatorv1alpha1.ResultError {
			return result, true, err
		}
		// Store the last non-compliance, so we can output that if
		// there were no errors.
		if result == complianceoperatorv1alpha1.ResultNonCompliant {
			lastNonCompliance = result
			compliant = false
		}

	}

	if !compliant {
		return lastNonCompliance, isReady, nil
	}
	return result, isReady, nil
}

func getPVCForScan(instance *complianceoperatorv1alpha1.ComplianceScan) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      getPVCForScanName(instance),
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"complianceScan": instance.Name,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			// NOTE(jaosorior): Currently we don't set a StorageClass
			// so the default will be taken into use.
			// TODO(jaosorior): Make StorageClass configurable
			StorageClassName: nil,
			AccessModes: []corev1.PersistentVolumeAccessMode{
				"ReadWriteOnce",
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					// TODO(jaosorior): Make this configurable
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}
}

// pod names are limited to 63 chars, inclusive. Try to use a friendly name, if that can't be done,
// just use a hash. Either way, the node would be present in a label of the pod.
func createPodForNodeName(scanName, nodeName string) string {
	return dnsLengthName("openscap-pod-", "%s-%s-pod", scanName, nodeName)
}

func getConfigMapForNodeName(scanName, nodeName string) string {
	return dnsLengthName("openscap-pod-", "%s-%s-pod", scanName, nodeName)
}

func getPodForNodeName(scanInstance *complianceoperatorv1alpha1.ComplianceScan, nodeName string) string {
	return scanInstance.Labels[nodePodLabel(nodeName)]
}

func setPodForNodeName(scanInstance *complianceoperatorv1alpha1.ComplianceScan, nodeName, podName string) {
	if scanInstance.Labels == nil {
		scanInstance.Labels = make(map[string]string)
	}

	// TODO(jaosorior): Figure out if we still need this after deleting the pods
	// This might be more appropraite as an annotation.
	scanInstance.Labels[nodePodLabel(nodeName)] = podName
}

func nodePodLabel(nodeName string) string {
	return OpenSCAPNodePodLabel + nodeName
}

func getPVCForScanName(instance *complianceoperatorv1alpha1.ComplianceScan) string {
	return instance.Name
}

func getInitContainerImage(scanSpec *complianceoperatorv1alpha1.ComplianceScanSpec, logger logr.Logger) string {
	image := DefaultContentContainerImage

	if scanSpec.ContentImage != "" {
		image = scanSpec.ContentImage
	}

	logger.Info("Content image", "image", image)
	return image
}
