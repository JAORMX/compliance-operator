package compliancescan

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	compv1alpha1 "github.com/openshift/compliance-operator/pkg/apis/compliance/v1alpha1"
	"github.com/openshift/compliance-operator/pkg/controller/common"
	"github.com/openshift/compliance-operator/pkg/utils"
)

const resultserverSA = "default"

// ReportsMount is the path where the reports will be mounted
const ReportsMount = "/reports"

// The result-server is a pod that listens for results from other pods and
// stores them in a PVC.
// It's comprised of the PVC for the scan, the pod and a service that fronts it
func (r *ReconcileComplianceScan) createResultServer(instance *compv1alpha1.ComplianceScan, logger logr.Logger) error {
	resultServerLabels := map[string]string{
		"complianceScan": instance.Name,
		"app":            "resultserver",
	}

	logger.Info("Creating scan result server pod")
	deployment := resultServer(instance, resultServerLabels, logger)
	err := r.client.Create(context.TODO(), deployment)
	if err != nil && !errors.IsAlreadyExists(err) {
		logger.Error(err, "Cannot create deployment", "deployment", deployment)
		return err
	}
	logger.Info("ResultServer Deployment launched", "Deployment.Name", deployment.Name)

	service := resultServerService(instance, resultServerLabels)
	err = r.client.Create(context.TODO(), service)
	if err != nil && !errors.IsAlreadyExists(err) {
		logger.Error(err, "Cannot create service", "service", service)
		return err
	}
	logger.Info("ResultServer Service launched", "Service.Name", service.Name)
	return nil
}

func (r *ReconcileComplianceScan) deleteResultServer(instance *compv1alpha1.ComplianceScan, logger logr.Logger) error {
	resultServerLabels := map[string]string{
		"complianceScan": instance.Name,
		"app":            "resultserver",
	}

	logger.Info("Deleting scan result server pod")

	deployment := resultServer(instance, resultServerLabels, logger)

	err := r.client.Delete(context.TODO(), deployment)
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Cannot delete deployment", "deployment", deployment)
		return err
	}
	logger.Info("ResultServer Deployment deleted", "Deployment.Name", deployment.Name)
	logger.Info("Deleting scan result server service")

	service := resultServerService(instance, resultServerLabels)
	err = r.client.Delete(context.TODO(), service)
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Cannot delete service", "service", service)
		return err
	}
	logger.Info("ResultServer Service deleted", "Service.Name", service.Name)
	return nil
}

// Serve up arf reports for a compliance scan with a web service protected by openshift auth (oauth-proxy sidecar).
// Needs corresponding Service (with service-serving cert).
// Need to aggregate reports into one service ? on subdirs?
func resultServer(scanInstance *compv1alpha1.ComplianceScan, labels map[string]string, logger logr.Logger) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      getResultServerName(scanInstance),
			Namespace: common.GetComplianceOperatorNamespace(),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &oneReplica,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					// TODO(jaosorior): Should we schedule this in the master nodes only?
					ServiceAccountName: resultserverSA,
					Containers: []corev1.Container{
						{
							Name:            "result-server",
							Image:           utils.GetComponentImage(utils.OPERATOR),
							ImagePullPolicy: corev1.PullAlways,
							Command: []string{
								"compliance-operator", "resultserver",
								fmt.Sprintf("--path=%s/", ReportsMount),
								"--address=0.0.0.0",
								fmt.Sprintf("--port=%d", ResultServerPort),
								fmt.Sprintf("--scan-index=%d", scanInstance.Status.CurrentIndex),
								fmt.Sprintf("--rotation=%d", scanInstance.Spec.RawResultStorageRotation),
								"--tls-server-cert=/etc/pki/tls/tls.crt",
								"--tls-server-key=/etc/pki/tls/tls.key",
								"--tls-ca=/etc/pki/tls/ca.crt",
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "arfreports",
									MountPath: ReportsMount,
								},
								{
									Name:      "tls",
									MountPath: "/etc/pki/tls",
									ReadOnly:  true,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "arfreports",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: getPVCForScanName(scanInstance.Name),
								},
							},
						},
						{
							Name: "tls",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: ServerCertPrefix + scanInstance.Name,
								},
							},
						},
					},
				},
			},
		},
	}
}

func resultServerService(scanInstance *compv1alpha1.ComplianceScan, labels map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      getResultServerName(scanInstance),
			Namespace: common.GetComplianceOperatorNamespace(),
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{
					Protocol: corev1.Protocol("TCP"),
					Port:     ResultServerPort,
				},
			},
		},
	}
}

func getResultServerName(instance *compv1alpha1.ComplianceScan) string {
	return instance.Name + "-rs"
}

func getResultServerURI(instance *compv1alpha1.ComplianceScan) string {
	return "https://" + getResultServerName(instance) + fmt.Sprintf(":%d/", ResultServerPort)
}
