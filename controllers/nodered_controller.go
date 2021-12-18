/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	cachev1alpha1 "github.com/jodevsa/nodered-operator/api/v1alpha1"
)

// NoderedReconciler reconciles a Nodered object
type NoderedReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=cache.example.com,resources=nodereds,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cache.example.com,resources=nodereds/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=cache.example.com,resources=nodereds/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Nodered object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.10.0/pkg/reconcile
func (r *NoderedReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)

	// Fetch the Nodered instance
	nodered := &cachev1alpha1.Nodered{}
	err := r.Get(ctx, req.NamespacedName, nodered)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("Nodered resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get Nodered")
		return ctrl.Result{}, err
	}

	found := &appsv1.StatefulSet{}
	err = r.Get(ctx, types.NamespacedName{Name: nodered.Name, Namespace: nodered.Namespace}, found)
	if err != nil && errors.IsNotFound(err) {
		// Define a new Statefulset
		dep := r.statefulSetForNodered(nodered)
		log.Info("Creating a new StatefulSet", "Statefulset.Namespace", dep.Namespace, "Statefulset.Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			log.Error(err, "Failed to create new Statefulset", "Statefulset.Namespace", dep.Namespace, "Statefulset.Name", dep.Name)
			return ctrl.Result{}, err
		}

		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Statefulset")
		return ctrl.Result{}, err
	}

	svcFound := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: nodered.Name + "-service", Namespace: nodered.Namespace}, svcFound)
	if err != nil && errors.IsNotFound(err) {

		svc := r.serviceForNodered(nodered)
		log.Info("Creating a new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
		err = r.Create(ctx, svc)
		if err != nil {
			log.Error(err, "Failed to create new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
			return ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Service")
		return ctrl.Result{}, err
	}

	ingressList := svcFound.Status.LoadBalancer.Ingress

	if len(ingressList) > 0 {
		ingressurl := svcFound.Status.LoadBalancer.Ingress[0].Hostname

		if nodered.Status.PublicUrl != ingressurl {
			nodered.Status.PublicUrl = ingressurl
			err = r.Status().Update(ctx, nodered)
			if err != nil {
				log.Error(err, "Failed to update Nodered status")
				return ctrl.Result{}, err
			}
		}
	} else {
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{}, nil
}

func (r *NoderedReconciler) serviceForNodered(m *cachev1alpha1.Nodered) *corev1.Service {
	labels := labelsForNodered(m.Name)

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-service",
			Namespace: m.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Protocol:   corev1.ProtocolTCP,
				Port:       80,
				TargetPort: intstr.FromInt(1880),
			}},
			Type: corev1.ServiceTypeLoadBalancer,
		},
	}
	// Set Nodered instance as the owner and controller
	ctrl.SetControllerReference(m, dep, r.Scheme)
	return dep
}

// deploymentForNodered returns a nodered Deployment object
func (r *NoderedReconciler) statefulSetForNodered(m *cachev1alpha1.Nodered) *appsv1.StatefulSet {
	ls := labelsForNodered(m.Name)
	replicas := int32(1)
	userId := int64(1000)
	rootUserId := int64(0)

	dep := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser: &userId,
						FSGroup:   &userId,
					},
					InitContainers: []corev1.Container{{
						Image:   "busybox:latest",
						Name:    "init",
						Command: []string{"sh", "-c", "mkdir -p /data && chown -R 1000:1000 /data"},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "data",
							MountPath: "/data",
						}},

						SecurityContext: &corev1.SecurityContext{
							RunAsUser: &rootUserId,
						},
					}},
					Containers: []corev1.Container{{
						Image: "nodered/node-red",
						Name:  "nodered",
						Ports: []corev1.ContainerPort{{
							ContainerPort: 1880,
							Name:          "nodered",
						}},
						Env: []corev1.EnvVar{{
							Name:  "node_red_data",
							Value: "/data",
						}},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "data",
							MountPath: "/data",
						}},
					}},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "data"},
				Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}},
			}},
		},
	}
	// Set Nodered instance as the owner and controller
	ctrl.SetControllerReference(m, dep, r.Scheme)
	return dep
}

// labelsForNodered returns the labels for selecting the resources
// belonging to the given nodered CR name.
func labelsForNodered(name string) map[string]string {
	return map[string]string{"app": "nodered", "nodered_cr": name}
}

// getPodNames returns the pod names of the array of pods passed in
func getPodNames(pods []corev1.Pod) []string {
	var podNames []string
	for _, pod := range pods {
		podNames = append(podNames, pod.Name)
	}
	return podNames
}

// SetupWithManager sets up the controller with the Manager.
func (r *NoderedReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cachev1alpha1.Nodered{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}
