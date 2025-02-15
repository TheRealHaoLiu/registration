package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"

	clusterv1client "open-cluster-management.io/api/client/cluster/clientset/versioned"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
)

const (
	apiserviceName  = "v1.admission.cluster.open-cluster-management.io"
	invalidURL      = "127.0.0.1:8001"
	validURL        = "https://127.0.0.1:8443"
	saNamespace     = "default"
	clusterSetLabel = "cluster.open-cluster-management.io/clusterset"
)

var _ = ginkgo.Describe("Admission webhook", func() {
	var admissionName string

	ginkgo.BeforeEach(func() {
		// make sure the api service v1.admission.cluster.open-cluster-management.io is available
		gomega.Eventually(func() bool {
			apiService, err := hubAPIServiceClient.APIServices().Get(context.TODO(), apiserviceName, metav1.GetOptions{})
			if err != nil {
				return false
			}
			if len(apiService.Status.Conditions) == 0 {
				return false
			}
			return apiService.Status.Conditions[0].Type == apiregistrationv1.Available &&
				apiService.Status.Conditions[0].Status == apiregistrationv1.ConditionTrue
		}, 60*time.Second, 1*time.Second).Should(gomega.BeTrue())
	})

	ginkgo.Context("ManagedCluster", func() {
		ginkgo.BeforeEach(func() {
			admissionName = "managedclustervalidators.admission.cluster.open-cluster-management.io"

			// make sure the managedcluster can be created successfully
			gomega.Eventually(func() bool {
				clusterName := fmt.Sprintf("webhook-spoke-%s", rand.String(6))
				managedCluster := newManagedCluster(clusterName, false, validURL)
				_, err := clusterClient.ClusterV1().ManagedClusters().Create(context.TODO(), managedCluster, metav1.CreateOptions{})
				if err != nil {
					return false
				}
				clusterClient.ClusterV1().ManagedClusters().Delete(context.TODO(), clusterName, metav1.DeleteOptions{})
				return true
			}, 60*time.Second, 1*time.Second).Should(gomega.BeTrue())
		})

		ginkgo.Context("Creating a managed cluster", func() {
			ginkgo.It("Should have the default LeaseDurationSeconds", func() {
				clusterName := fmt.Sprintf("webhook-spoke-%s", rand.String(6))
				ginkgo.By(fmt.Sprintf("create a managed cluster %q", clusterName))

				_, err := clusterClient.ClusterV1().ManagedClusters().Create(context.TODO(), newManagedCluster(clusterName, false, validURL), metav1.CreateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				managedCluster, err := clusterClient.ClusterV1().ManagedClusters().Get(context.TODO(), clusterName, metav1.GetOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(managedCluster.Spec.LeaseDurationSeconds).To(gomega.Equal(int32(60)))

				gomega.Expect(deleteManageClusterAndRelatedNamespace(clusterName)).ToNot(gomega.HaveOccurred())
			})

			ginkgo.It("Should have the timeAdded for taints", func() {
				clusterName := fmt.Sprintf("webhook-spoke-%s", rand.String(6))
				ginkgo.By(fmt.Sprintf("create a managed cluster %q with taint", clusterName))

				cluster := newManagedCluster(clusterName, false, validURL)
				cluster.Spec.Taints = []clusterv1.Taint{
					{
						Key:    "a",
						Value:  "b",
						Effect: clusterv1.TaintEffectNoSelect,
					},
				}
				_, err := clusterClient.ClusterV1().ManagedClusters().Create(context.TODO(), cluster, metav1.CreateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				ginkgo.By("check if timeAdded of the taint is set automatically")
				managedCluster, err := clusterClient.ClusterV1().ManagedClusters().Get(context.TODO(), clusterName, metav1.GetOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				taint := findTaint(managedCluster.Spec.Taints, "a", "b", clusterv1.TaintEffectNoSelect)
				gomega.Expect(taint).ShouldNot(gomega.BeNil())
				gomega.Expect(taint.TimeAdded.IsZero()).To(gomega.BeFalse())

				ginkgo.By("chang the effect of the taint")
				// sleep and make sure the update is performed 1 second later than the creation
				time.Sleep(1 * time.Second)
				gomega.Eventually(func() error {
					managedCluster, err := clusterClient.ClusterV1().ManagedClusters().Get(context.TODO(), clusterName, metav1.GetOptions{})
					if err != nil {
						return err
					}
					for index, taint := range managedCluster.Spec.Taints {
						if taint.Key != "a" {
							continue
						}
						if taint.Value != "b" {
							continue
						}
						managedCluster.Spec.Taints[index] = clusterv1.Taint{
							Key:    taint.Key,
							Value:  taint.Value,
							Effect: clusterv1.TaintEffectNoSelectIfNew,
						}
					}
					_, err = clusterClient.ClusterV1().ManagedClusters().Update(context.TODO(), managedCluster, metav1.UpdateOptions{})
					return err
				}, 60*time.Second, 1*time.Second).Should(gomega.Succeed())

				ginkgo.By("check if timeAdded of the taint is reset")
				managedCluster, err = clusterClient.ClusterV1().ManagedClusters().Get(context.TODO(), clusterName, metav1.GetOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				updatedTaint := findTaint(managedCluster.Spec.Taints, "a", "b", clusterv1.TaintEffectNoSelectIfNew)
				gomega.Expect(updatedTaint).ShouldNot(gomega.BeNil())
				gomega.Expect(taint.TimeAdded.Equal(&updatedTaint.TimeAdded)).To(gomega.BeFalse(),
					"timeAdded of taint should be updated (before=%s, after=%s)", taint.TimeAdded.Time.String(), updatedTaint.TimeAdded.Time.String())

				gomega.Expect(deleteManageClusterAndRelatedNamespace(clusterName)).ToNot(gomega.HaveOccurred())
			})

			ginkgo.It("Should respond bad request when creating a managed cluster with invalid external server URLs", func() {
				clusterName := fmt.Sprintf("webhook-spoke-%s", rand.String(6))
				ginkgo.By(fmt.Sprintf("create a managed cluster %q with an invalid external server URL %q", clusterName, invalidURL))

				managedCluster := newManagedCluster(clusterName, false, invalidURL)

				_, err := clusterClient.ClusterV1().ManagedClusters().Create(context.TODO(), managedCluster, metav1.CreateOptions{})
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(errors.IsBadRequest(err)).Should(gomega.BeTrue())
				gomega.Expect(err.Error()).Should(gomega.Equal(fmt.Sprintf(
					"admission webhook \"%s\" denied the request: url \"%s\" is invalid in client configs",
					admissionName,
					invalidURL,
				)))

				gomega.Expect(deleteManageClusterAndRelatedNamespace(clusterName)).ToNot(gomega.HaveOccurred())
			})

			ginkgo.It("Should forbid the request when creating an accepted managed cluster by unauthorized user", func() {
				sa := fmt.Sprintf("webhook-sa-%s", rand.String(6))
				clusterName := fmt.Sprintf("webhook-spoke-%s", rand.String(6))

				ginkgo.By(fmt.Sprintf("create an managed cluster %q with unauthorized service account %q", clusterName, sa))

				// prepare an unauthorized cluster client from a service account who can create/get/update ManagedCluster
				// but cannot change the ManagedCluster HubAcceptsClient field
				unauthorizedClient, err := buildClusterClient(saNamespace, sa, []rbacv1.PolicyRule{
					{
						APIGroups: []string{"cluster.open-cluster-management.io"},
						Resources: []string{"managedclusters"},
						Verbs:     []string{"create", "get", "update"},
					},
				}, nil)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				managedCluster := newManagedCluster(clusterName, true, validURL)

				_, err = unauthorizedClient.ClusterV1().ManagedClusters().Create(context.TODO(), managedCluster, metav1.CreateOptions{})
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue())
				gomega.Expect(err.Error()).Should(gomega.Equal(fmt.Sprintf(
					"admission webhook \"%s\" denied the request: user \"system:serviceaccount:%s:%s\" cannot update the HubAcceptsClient field",
					admissionName,
					saNamespace,
					sa,
				)))

				gomega.Expect(deleteManageClusterAndRelatedNamespace(clusterName)).ToNot(gomega.HaveOccurred())
				gomega.Expect(cleanupClusterClient(saNamespace, sa)).ToNot(gomega.HaveOccurred())
			})

			ginkgo.It("Should accept the request when creating an accepted managed cluster by authorized user", func() {
				sa := fmt.Sprintf("webhook-sa-%s", rand.String(6))
				clusterName := fmt.Sprintf("webhook-spoke-%s", rand.String(6))

				ginkgo.By(fmt.Sprintf("create an managed cluster %q with authorized service account %q", clusterName, sa))
				authorizedClient, err := buildClusterClient(saNamespace, sa, []rbacv1.PolicyRule{
					{
						APIGroups: []string{"cluster.open-cluster-management.io"},
						Resources: []string{"managedclusters"},
						Verbs:     []string{"create", "get", "update"},
					},
					{
						APIGroups:     []string{"register.open-cluster-management.io"},
						Resources:     []string{"managedclusters/accept"},
						ResourceNames: []string{clusterName},
						Verbs:         []string{"update"},
					},
				}, nil)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				managedCluster := newManagedCluster(clusterName, true, validURL)
				_, err = authorizedClient.ClusterV1().ManagedClusters().Create(context.TODO(), managedCluster, metav1.CreateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				gomega.Expect(deleteManageClusterAndRelatedNamespace(clusterName)).ToNot(gomega.HaveOccurred())
				gomega.Expect(cleanupClusterClient(saNamespace, sa)).ToNot(gomega.HaveOccurred())
			})

			ginkgo.It("Should accept the request when creating a managed cluster with clusterset specified by authorized user", func() {
				clusterSetName := fmt.Sprintf("webhook-spoke-%s", rand.String(6))
				ginkgo.By(fmt.Sprintf("create a managed cluster set %q", clusterSetName))

				managedClusterSet := &clusterv1beta1.ManagedClusterSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: clusterSetName,
					},
				}

				_, err := clusterClient.ClusterV1beta1().ManagedClusterSets().Create(context.TODO(), managedClusterSet, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				sa := fmt.Sprintf("webhook-sa-%s", rand.String(6))
				clusterName := fmt.Sprintf("webhook-spoke-%s", rand.String(6))

				ginkgo.By(fmt.Sprintf("create a managed cluster %q with unauthorized service account %q", clusterName, sa))

				authorizedClient, err := buildClusterClient(saNamespace, sa, []rbacv1.PolicyRule{
					{
						APIGroups: []string{"cluster.open-cluster-management.io"},
						Resources: []string{"managedclusters"},
						Verbs:     []string{"create", "get", "update"},
					},
					{
						APIGroups: []string{"cluster.open-cluster-management.io"},
						Resources: []string{"managedclustersets/join"},
						Verbs:     []string{"create"},
					},
				}, nil)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				managedCluster := newManagedCluster(clusterName, false, validURL)
				managedCluster.Labels = map[string]string{
					clusterSetLabel: clusterSetName,
				}
				_, err = authorizedClient.ClusterV1().ManagedClusters().Create(context.TODO(), managedCluster, metav1.CreateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				gomega.Expect(deleteManageClusterAndRelatedNamespace(clusterName)).ToNot(gomega.HaveOccurred())
				gomega.Expect(cleanupClusterClient(saNamespace, sa)).ToNot(gomega.HaveOccurred())
			})

			ginkgo.It("Should forbid the request when creating a managed cluster with clusterset specified by unauthorized user", func() {
				clusterSetName := fmt.Sprintf("webhook-spoke-%s", rand.String(6))
				ginkgo.By(fmt.Sprintf("create a managed cluster set %q", clusterSetName))

				managedClusterSet := &clusterv1beta1.ManagedClusterSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: clusterSetName,
					},
				}

				_, err := clusterClient.ClusterV1beta1().ManagedClusterSets().Create(context.TODO(), managedClusterSet, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				sa := fmt.Sprintf("webhook-sa-%s", rand.String(6))
				clusterName := fmt.Sprintf("webhook-spoke-%s", rand.String(6))

				ginkgo.By(fmt.Sprintf("create a managed cluster %q with unauthorized service account %q", clusterName, sa))

				// prepare an unauthorized cluster client from a service account who can create/get/update ManagedCluster
				// but cannot set the clusterset label
				unauthorizedClient, err := buildClusterClient(saNamespace, sa, []rbacv1.PolicyRule{
					{
						APIGroups: []string{"cluster.open-cluster-management.io"},
						Resources: []string{"managedclusters"},
						Verbs:     []string{"create", "get", "update"},
					},
				}, nil)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				managedCluster := newManagedCluster(clusterName, false, validURL)
				managedCluster.Labels = map[string]string{
					clusterSetLabel: clusterSetName,
				}
				_, err = unauthorizedClient.ClusterV1().ManagedClusters().Create(context.TODO(), managedCluster, metav1.CreateOptions{})
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue())
				gomega.Expect(err.Error()).Should(gomega.Equal(fmt.Sprintf(
					"admission webhook \"%s\" denied the request: user \"system:serviceaccount:%s:%s\" cannot add/remove a ManagedCluster to/from ManagedClusterSet \"%s\"",
					admissionName,
					saNamespace,
					sa,
					clusterSetName,
				)))

				gomega.Expect(deleteManageClusterAndRelatedNamespace(clusterName)).ToNot(gomega.HaveOccurred())
				gomega.Expect(cleanupClusterClient(saNamespace, sa)).ToNot(gomega.HaveOccurred())
			})
		})

		ginkgo.Context("Updating a managed cluster", func() {
			var clusterName string
			ginkgo.BeforeEach(func() {
				ginkgo.By(fmt.Sprintf("Creating managed cluster %q", clusterName))
				clusterName = fmt.Sprintf("webhook-spoke-%s", rand.String(6))
				managedCluster := newManagedCluster(clusterName, false, validURL)
				_, err := clusterClient.ClusterV1().ManagedClusters().Create(context.TODO(), managedCluster, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			})

			ginkgo.AfterEach(func() {
				ginkgo.By(fmt.Sprintf("Cleaning managed cluster %q", clusterName))
				gomega.Expect(deleteManageClusterAndRelatedNamespace(clusterName)).ToNot(gomega.HaveOccurred())
			})

			ginkgo.It("Should not update the LeaseDurationSeconds to zero", func() {
				ginkgo.By(fmt.Sprintf("try to update managed cluster %q LeaseDurationSeconds to zero", clusterName))
				err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
					managedCluster, err := clusterClient.ClusterV1().ManagedClusters().Get(context.TODO(), clusterName, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())

					managedCluster.Spec.LeaseDurationSeconds = 0
					_, err = clusterClient.ClusterV1().ManagedClusters().Update(context.TODO(), managedCluster, metav1.UpdateOptions{})
					return err
				})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				managedCluster, err := clusterClient.ClusterV1().ManagedClusters().Get(context.TODO(), clusterName, metav1.GetOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(managedCluster.Spec.LeaseDurationSeconds).To(gomega.Equal(int32(60)))
			})

			ginkgo.It("Should respond bad request when updating a managed cluster with invalid external server URLs", func() {
				ginkgo.By(fmt.Sprintf("update managed cluster %q with an invalid external server URL %q", clusterName, invalidURL))

				err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
					managedCluster, err := clusterClient.ClusterV1().ManagedClusters().Get(context.TODO(), clusterName, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())

					managedCluster.Spec.ManagedClusterClientConfigs[0].URL = invalidURL
					_, err = clusterClient.ClusterV1().ManagedClusters().Update(context.TODO(), managedCluster, metav1.UpdateOptions{})
					return err
				})
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(errors.IsBadRequest(err)).Should(gomega.BeTrue())
				gomega.Expect(err.Error()).Should(gomega.Equal(fmt.Sprintf(
					"admission webhook \"%s\" denied the request: url \"%s\" is invalid in client configs",
					admissionName,
					invalidURL,
				)))
			})

			ginkgo.It("Should forbid the request when updating an unaccepted managed cluster to accepted by unauthorized user", func() {
				sa := fmt.Sprintf("webhook-sa-%s", rand.String(6))
				ginkgo.By(fmt.Sprintf("accept managed cluster %q by an unauthorized user %q", clusterName, sa))

				// prepare an unauthorized cluster client from a service account who can create/get/update ManagedCluster
				// but cannot change the ManagedCluster HubAcceptsClient field
				unauthorizedClient, err := buildClusterClient(saNamespace, sa, []rbacv1.PolicyRule{
					{
						APIGroups: []string{"cluster.open-cluster-management.io"},
						Resources: []string{"managedclusters"},
						Verbs:     []string{"create", "get", "update"},
					},
				}, nil)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
					managedCluster, err := unauthorizedClient.ClusterV1().ManagedClusters().Get(context.TODO(), clusterName, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())

					managedCluster.Spec.HubAcceptsClient = true
					_, err = unauthorizedClient.ClusterV1().ManagedClusters().Update(context.TODO(), managedCluster, metav1.UpdateOptions{})
					return err
				})
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue())
				gomega.Expect(err.Error()).Should(gomega.Equal(fmt.Sprintf(
					"admission webhook \"%s\" denied the request: user \"system:serviceaccount:%s:%s\" cannot update the HubAcceptsClient field",
					admissionName,
					saNamespace,
					sa,
				)))

				gomega.Expect(cleanupClusterClient(saNamespace, sa)).ToNot(gomega.HaveOccurred())
			})

			ginkgo.It("Should accept the request when updating the clusterset of a managed cluster by authorized user", func() {
				clusterSetName := fmt.Sprintf("webhook-spoke-%s", rand.String(6))
				ginkgo.By(fmt.Sprintf("create a managed cluster set %q", clusterSetName))

				managedClusterSet := &clusterv1beta1.ManagedClusterSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: clusterSetName,
					},
				}

				_, err := clusterClient.ClusterV1beta1().ManagedClusterSets().Create(context.TODO(), managedClusterSet, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				sa := fmt.Sprintf("webhook-sa-%s", rand.String(6))
				ginkgo.By(fmt.Sprintf("accept managed cluster %q by an unauthorized user %q", clusterName, sa))

				authorizedClient, err := buildClusterClient(saNamespace, sa, []rbacv1.PolicyRule{
					{
						APIGroups: []string{"cluster.open-cluster-management.io"},
						Resources: []string{"managedclusters"},
						Verbs:     []string{"create", "get", "update"},
					},
					{
						APIGroups: []string{"cluster.open-cluster-management.io"},
						Resources: []string{"managedclustersets/join"},
						Verbs:     []string{"create"},
					},
				}, nil)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
					managedCluster, err := authorizedClient.ClusterV1().ManagedClusters().Get(context.TODO(), clusterName, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())

					managedCluster.Labels = map[string]string{
						clusterSetLabel: clusterSetName,
					}
					_, err = authorizedClient.ClusterV1().ManagedClusters().Update(context.TODO(), managedCluster, metav1.UpdateOptions{})
					return err
				})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				gomega.Expect(cleanupClusterClient(saNamespace, sa)).ToNot(gomega.HaveOccurred())
			})

			ginkgo.It("Should forbid the request when updating the clusterset of a managed cluster by unauthorized user", func() {
				clusterSetName := fmt.Sprintf("webhook-spoke-%s", rand.String(6))
				ginkgo.By(fmt.Sprintf("create a managed cluster set %q", clusterSetName))

				managedClusterSet := &clusterv1beta1.ManagedClusterSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: clusterSetName,
					},
				}

				_, err := clusterClient.ClusterV1beta1().ManagedClusterSets().Create(context.TODO(), managedClusterSet, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				sa := fmt.Sprintf("webhook-sa-%s", rand.String(6))
				ginkgo.By(fmt.Sprintf("accept managed cluster %q by an unauthorized user %q", clusterName, sa))

				// prepare an unauthorized cluster client from a service account who can create/get/update ManagedCluster
				// but cannot change the clusterset label
				unauthorizedClient, err := buildClusterClient(saNamespace, sa, []rbacv1.PolicyRule{
					{
						APIGroups: []string{"cluster.open-cluster-management.io"},
						Resources: []string{"managedclusters"},
						Verbs:     []string{"create", "get", "update"},
					},
				}, nil)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
					managedCluster, err := unauthorizedClient.ClusterV1().ManagedClusters().Get(context.TODO(), clusterName, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())

					managedCluster.Labels = map[string]string{
						clusterSetLabel: clusterSetName,
					}
					_, err = unauthorizedClient.ClusterV1().ManagedClusters().Update(context.TODO(), managedCluster, metav1.UpdateOptions{})
					return err
				})
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue())
				gomega.Expect(err.Error()).Should(gomega.Equal(fmt.Sprintf(
					"admission webhook \"%s\" denied the request: user \"system:serviceaccount:%s:%s\" cannot add/remove a ManagedCluster to/from ManagedClusterSet \"%s\"",
					admissionName,
					saNamespace,
					sa,
					clusterSetName,
				)))

				gomega.Expect(cleanupClusterClient(saNamespace, sa)).ToNot(gomega.HaveOccurred())
			})
		})
	})

	ginkgo.Context("ManagedClusterSetBinding", func() {
		var namespace string

		ginkgo.BeforeEach(func() {
			admissionName = "managedclustersetbindingvalidators.admission.cluster.open-cluster-management.io"

			// create a namespace for testing
			namespace = fmt.Sprintf("ns-%s", rand.String(6))
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_, err := hubClient.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// make sure the managedclusterset can be created successfully
			gomega.Eventually(func() bool {
				clusterSetName := fmt.Sprintf("clusterset-%s", rand.String(6))
				managedClusterSetBinding := newManagedClusterSetBinding(namespace, clusterSetName, clusterSetName)
				_, err := clusterClient.ClusterV1beta1().ManagedClusterSetBindings(namespace).Create(context.TODO(), managedClusterSetBinding, metav1.CreateOptions{})
				if err != nil {
					return false
				}
				clusterClient.ClusterV1beta1().ManagedClusterSetBindings(namespace).Delete(context.TODO(), clusterSetName, metav1.DeleteOptions{})
				return true
			}, 60*time.Second, 1*time.Second).Should(gomega.BeTrue())
		})

		ginkgo.AfterEach(func() {
			hubClient.CoreV1().Namespaces().Delete(context.TODO(), namespace, metav1.DeleteOptions{})
		})

		ginkgo.Context("Creating a ManagedClusterSetBinding", func() {
			ginkgo.It("should deny the request when creating a ManagedClusterSetBinding with unmatched cluster set name", func() {
				clusterSetName := fmt.Sprintf("clusterset-%s", rand.String(6))
				clusterSetBindingName := fmt.Sprintf("clustersetbinding-%s", rand.String(6))
				managedClusterSetBinding := newManagedClusterSetBinding(namespace, clusterSetBindingName, clusterSetName)
				_, err := clusterClient.ClusterV1beta1().ManagedClusterSetBindings(namespace).Create(context.TODO(), managedClusterSetBinding, metav1.CreateOptions{})
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(errors.IsBadRequest(err)).Should(gomega.BeTrue())
				gomega.Expect(err.Error()).Should(gomega.Equal(fmt.Sprintf(
					"admission webhook \"%s\" denied the request: The ManagedClusterSetBinding must have the same name as the target ManagedClusterSet",
					admissionName,
				)))
			})

			ginkgo.It("should accept the request when creating a ManagedClusterSetBinding by authorized user", func() {
				sa := fmt.Sprintf("webhook-sa-%s", rand.String(6))
				clusterSetName := fmt.Sprintf("clusterset-%s", rand.String(6))

				authorizedClient, err := buildClusterClient(namespace, sa, []rbacv1.PolicyRule{
					{
						APIGroups: []string{"cluster.open-cluster-management.io"},
						Resources: []string{"managedclustersets/bind"},
						Verbs:     []string{"create"},
					},
				}, []rbacv1.PolicyRule{
					{
						APIGroups: []string{"cluster.open-cluster-management.io"},
						Resources: []string{"managedclustersetbindings"},
						Verbs:     []string{"create", "get", "update"},
					},
				})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				managedClusterSetBinding := newManagedClusterSetBinding(namespace, clusterSetName, clusterSetName)
				_, err = authorizedClient.ClusterV1beta1().ManagedClusterSetBindings(namespace).Create(context.TODO(), managedClusterSetBinding, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				gomega.Expect(cleanupClusterClient(namespace, sa)).ToNot(gomega.HaveOccurred())
			})

			ginkgo.It("should forbid the request when creating a ManagedClusterSetBinding by unauthorized user", func() {
				sa := fmt.Sprintf("webhook-sa-%s", rand.String(6))
				clusterSetName := fmt.Sprintf("clusterset-%s", rand.String(6))

				// prepare an unauthorized cluster client from a service account who can create/get/update ManagedClusterSetBinding
				// but cannot bind ManagedClusterSet
				unauthorizedClient, err := buildClusterClient(namespace, sa, nil, []rbacv1.PolicyRule{
					{
						APIGroups: []string{"cluster.open-cluster-management.io"},
						Resources: []string{"managedclustersetbindings"},
						Verbs:     []string{"create", "get", "update"},
					},
				})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				managedClusterSetBinding := newManagedClusterSetBinding(namespace, clusterSetName, clusterSetName)
				_, err = unauthorizedClient.ClusterV1beta1().ManagedClusterSetBindings(namespace).Create(context.TODO(), managedClusterSetBinding, metav1.CreateOptions{})
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue())
				gomega.Expect(err.Error()).Should(gomega.Equal(fmt.Sprintf(
					"admission webhook \"%s\" denied the request: user \"system:serviceaccount:%s:%s\" is not allowed to bind cluster set \"%s\"",
					admissionName,
					namespace,
					sa,
					clusterSetName,
				)))

				gomega.Expect(cleanupClusterClient(namespace, sa)).ToNot(gomega.HaveOccurred())
			})
		})

		ginkgo.Context("Updating a ManagedClusterSetBinding", func() {
			ginkgo.It("should deny the request when updating a ManagedClusterSetBinding with a new cluster set", func() {
				// create a cluster set binding
				clusterSetName := fmt.Sprintf("clusterset-%s", rand.String(6))
				managedClusterSetBinding := newManagedClusterSetBinding(namespace, clusterSetName, clusterSetName)
				managedClusterSetBinding, err := clusterClient.ClusterV1beta1().ManagedClusterSetBindings(namespace).Create(context.TODO(), managedClusterSetBinding, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				// update the cluster set binding
				clusterSetName = fmt.Sprintf("clusterset-%s", rand.String(6))
				managedClusterSetBinding.Spec.ClusterSet = clusterSetName
				_, err = clusterClient.ClusterV1beta1().ManagedClusterSetBindings(namespace).Update(context.TODO(), managedClusterSetBinding, metav1.UpdateOptions{})
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(errors.IsBadRequest(err)).Should(gomega.BeTrue())
				gomega.Expect(err.Error()).Should(gomega.Equal(fmt.Sprintf(
					"admission webhook \"%s\" denied the request: The ManagedClusterSetBinding must have the same name as the target ManagedClusterSet",
					admissionName,
				)))
			})

			ginkgo.It("should accept the request when updating the label of the ManagedClusterSetBinding by user without binding permission", func() {
				// create a cluster set binding
				clusterSetName := fmt.Sprintf("clusterset-%s", rand.String(6))
				managedClusterSetBinding := newManagedClusterSetBinding(namespace, clusterSetName, clusterSetName)
				managedClusterSetBinding, err := clusterClient.ClusterV1beta1().ManagedClusterSetBindings(namespace).Create(context.TODO(), managedClusterSetBinding, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				// create a client without clusterset binding permission
				sa := fmt.Sprintf("webhook-sa-%s", rand.String(6))
				unauthorizedClient, err := buildClusterClient(namespace, sa, nil, []rbacv1.PolicyRule{
					{
						APIGroups: []string{"cluster.open-cluster-management.io"},
						Resources: []string{"managedclustersetbindings"},
						Verbs:     []string{"create", "get", "update"},
					},
				})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				// update the cluster set binding by authorized user
				managedClusterSetBinding.Labels = map[string]string{
					"owner": "user1",
				}
				_, err = unauthorizedClient.ClusterV1beta1().ManagedClusterSetBindings(namespace).Update(context.TODO(), managedClusterSetBinding, metav1.UpdateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				gomega.Expect(cleanupClusterClient(namespace, sa)).ToNot(gomega.HaveOccurred())
			})
		})
	})
})

func newManagedCluster(name string, accepted bool, externalURL string) *clusterv1.ManagedCluster {
	return &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: clusterv1.ManagedClusterSpec{
			HubAcceptsClient: accepted,
			ManagedClusterClientConfigs: []clusterv1.ClientConfig{
				{
					URL: externalURL,
				},
			},
		},
	}
}

func newManagedClusterSetBinding(namespace, name string, clusterSet string) *clusterv1beta1.ManagedClusterSetBinding {
	return &clusterv1beta1.ManagedClusterSetBinding{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: clusterv1beta1.ManagedClusterSetBindingSpec{
			ClusterSet: clusterSet,
		},
	}
}

func buildClusterClient(saNamespace, saName string, clusterPolicyRules, policyRules []rbacv1.PolicyRule) (clusterv1client.Interface, error) {
	var err error

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: saNamespace,
			Name:      saName,
		},
	}
	_, err = hubClient.CoreV1().ServiceAccounts(saNamespace).Create(context.TODO(), sa, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	// create cluster role/rolebinding
	if len(clusterPolicyRules) > 0 {
		clusterRoleName := fmt.Sprintf("%s-clusterrole", saName)
		clusterRole := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterRoleName,
			},
			Rules: clusterPolicyRules,
		}
		_, err = hubClient.RbacV1().ClusterRoles().Create(context.TODO(), clusterRole, metav1.CreateOptions{})
		if err != nil {
			return nil, err
		}

		clusterRoleBinding := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s-clusterrolebinding", saName),
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Namespace: saNamespace,
					Name:      saName,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     clusterRoleName,
			},
		}
		_, err = hubClient.RbacV1().ClusterRoleBindings().Create(context.TODO(), clusterRoleBinding, metav1.CreateOptions{})
		if err != nil {
			return nil, err
		}
	}

	// create cluster role/rolebinding
	if len(policyRules) > 0 {
		roleName := fmt.Sprintf("%s-role", saName)
		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: saNamespace,
				Name:      roleName,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{"cluster.open-cluster-management.io"},
					Resources: []string{"managedclustersetbindings"},
					Verbs:     []string{"create", "get", "update"},
				},
			},
		}
		_, err = hubClient.RbacV1().Roles(saNamespace).Create(context.TODO(), role, metav1.CreateOptions{})
		if err != nil {
			return nil, err
		}

		roleBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: saNamespace,
				Name:      fmt.Sprintf("%s-rolebinding", saName),
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Namespace: saNamespace,
					Name:      saName,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     roleName,
			},
		}
		_, err = hubClient.RbacV1().RoleBindings(saNamespace).Create(context.TODO(), roleBinding, metav1.CreateOptions{})
		if err != nil {
			return nil, err
		}
	}

	var tokenSecret *corev1.Secret
	err = wait.Poll(1*time.Second, 5*time.Second, func() (bool, error) {
		secrets, err := hubClient.CoreV1().Secrets(saNamespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		for _, secret := range secrets.Items {
			if strings.HasPrefix(secret.Name, fmt.Sprintf("%s-token-", saName)) {
				tokenSecret = &secret
				return true, nil
			}
		}

		return false, nil
	})
	if err != nil {
		return nil, err
	}
	if tokenSecret == nil {
		return nil, fmt.Errorf("the %s token secret cannot be found in %s namespace", sa, saNamespace)
	}

	unauthorizedClusterClient, err := clusterv1client.NewForConfig(&restclient.Config{
		Host: clusterCfg.Host,
		TLSClientConfig: restclient.TLSClientConfig{
			CAData: clusterCfg.CAData,
		},
		BearerToken: string(tokenSecret.Data["token"]),
	})
	return unauthorizedClusterClient, err
}

// cleanupClusterClient delete cluster-scope resource created by func "buildClusterClient",
// the namespace-scope resources should be deleted by an additional namespace deleting func.
// It is recommended be invoked as a pair with the func "buildClusterClient"
func cleanupClusterClient(saNamespace, saName string) error {
	err := hubClient.CoreV1().ServiceAccounts(saNamespace).Delete(context.TODO(), saName, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("delete sa %q/%q failed: %v", saNamespace, saName, err)
	}

	// delete cluster role and cluster role binding if exists
	clusterRoleName := fmt.Sprintf("%s-clusterrole", saName)
	err = hubClient.RbacV1().ClusterRoles().Delete(context.TODO(), clusterRoleName, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete cluster role %q failed: %v", clusterRoleName, err)
	}
	clusterRoleBindingName := fmt.Sprintf("%s-clusterrolebinding", saName)
	err = hubClient.RbacV1().ClusterRoleBindings().Delete(context.TODO(), clusterRoleBindingName, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete cluster role binding %q failed: %v", clusterRoleBindingName, err)
	}

	return nil
}

// findTaint returns the first matched taint in the given taint array. Return nil if no taint matched.
func findTaint(taints []clusterv1.Taint, key, value string, effect clusterv1.TaintEffect) *clusterv1.Taint {
	for _, taint := range taints {
		if len(key) != 0 && taint.Key != key {
			continue
		}

		if len(value) != 0 && taint.Value != value {
			continue
		}

		if len(effect) != 0 && taint.Effect != effect {
			continue
		}

		return &taint
	}
	return nil
}
