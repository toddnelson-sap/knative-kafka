package kafkachannel

import (
	"fmt"
	"github.com/kyma-incubator/knative-kafka/components/common/pkg/health"
	kafkautil "github.com/kyma-incubator/knative-kafka/components/common/pkg/kafka/util"
	"github.com/kyma-incubator/knative-kafka/components/controller/constants"
	knativekafkav1alpha1 "github.com/kyma-incubator/knative-kafka/components/controller/pkg/apis/knativekafka/v1alpha1"
	"github.com/kyma-incubator/knative-kafka/components/controller/pkg/env"
	"github.com/kyma-incubator/knative-kafka/components/controller/pkg/event"
	"github.com/kyma-incubator/knative-kafka/components/controller/pkg/util"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	eventingNames "knative.dev/eventing/pkg/reconciler/names"
	eventingUtils "knative.dev/eventing/pkg/utils"
	"knative.dev/pkg/apis"
	"strconv"
)

// Reconcile The "Channel" Inbound For The Specified Channel (K8S Service, Deployment)
func (r *Reconciler) reconcileChannel(channel *knativekafkav1alpha1.KafkaChannel) error {

	// Get Channel Specific Logger
	logger := util.ChannelLogger(r.Logger.Desugar(), channel)

	// If The Channel Is Being Deleted - Nothing To Do Since K8S Garbage Collection Will Cleanup Based On OwnerReference
	if channel.DeletionTimestamp != nil {
		logger.Info("Successfully Reconciled Channel Deletion")
		return nil
	}

	// Reconcile The KafkaChannel's Service
	channelServiceErr := r.reconcileKafkaChannelService(channel)
	if channelServiceErr != nil {
		r.Recorder.Eventf(channel, corev1.EventTypeWarning, event.ChannelServiceReconciliationFailed.String(), "Failed To Reconcile Channel Service (Channel): %v", channelServiceErr)
		logger.Error("Failed To Reconcile KafkaChannel Service", zap.Error(channelServiceErr))
	} else {
		logger.Info("Successfully Reconciled KafkaChannel Service")
	}

	// Reconcile The Channel Deployment's Service
	deploymentServiceErr := r.reconcileChannelDeploymentService(channel)
	if deploymentServiceErr != nil {
		r.Recorder.Eventf(channel, corev1.EventTypeWarning, event.ChannelServiceReconciliationFailed.String(), "Failed To Reconcile Channel Service (Deployment): %v", deploymentServiceErr)
		logger.Error("Failed To Reconcile Channel Deployment Service", zap.Error(deploymentServiceErr))
	} else {
		logger.Info("Successfully Reconciled Channel Deployment Service")
	}

	// Reconcile The Channel's Deployment
	deploymentErr := r.reconcileChannelDeployment(channel)
	if deploymentErr != nil {
		r.Recorder.Eventf(channel, corev1.EventTypeWarning, event.ChannelDeploymentReconciliationFailed.String(), "Failed To Reconcile Channel Deployment: %v", deploymentErr)
		logger.Error("Failed To Reconcile Channel Deployment", zap.Error(deploymentErr))
	} else {
		logger.Info("Successfully Reconciled Channel Deployment")
	}

	// Return Results
	if channelServiceErr != nil || deploymentServiceErr != nil || deploymentErr != nil {
		return fmt.Errorf("failed to reconcile channel components")
	} else {
		return nil // Success
	}
}

//
// KafkaChannel Kafka Channel Service
//
// One K8S Service per KafkaChannel, in the same namespace as the KafkaChannel, with an
// ExternalName reference to the single K8S Service in the knative-eventing namespace
// for the Channel Deployment/Pods (as reconciled below).
//

// Reconcile The KafkaChannel Service
func (r *Reconciler) reconcileKafkaChannelService(channel *knativekafkav1alpha1.KafkaChannel) error {

	// Attempt To Get The Service Associated With The Specified Channel
	service, err := r.getKafkaChannelService(channel)
	if err != nil {

		// If The Service Was Not Found - Then Create A New One For The Channel
		if errors.IsNotFound(err) {
			r.Logger.Info("KafkaChannel Service Not Found - Creating New One")
			service = r.newKafkaChannelService(channel)
			service, err = r.KubeClientSet.CoreV1().Services(service.Namespace).Create(service)
			if err != nil {
				r.Logger.Error("Failed To Create KafkaChannel Service", zap.Error(err))
				channel.Status.MarkChannelServiceFailed("ChannelServiceFailed", fmt.Sprintf("Channel Service Failed: %s", err))
				return err
			} else {
				r.Logger.Info("Successfully Created KafkaChannel Service")
				// Continue To Update Channel Status
			}
		} else {
			r.Logger.Error("Failed To Get KafkaChannel Service", zap.Error(err))
			channel.Status.MarkChannelServiceFailed("ChannelServiceFailed", fmt.Sprintf("Channel Service Failed: %s", err))
			return err
		}
	} else {
		r.Logger.Info("Successfully Verified KafkaChannel Service")
		// Continue To Update Channel Status
	}

	// Update Channel Status
	channel.Status.MarkChannelServiceTrue()
	channel.Status.SetAddress(&apis.URL{
		Scheme: "http",
		Host:   eventingNames.ServiceHostName(service.Name, service.Namespace),
	})

	// Return Success
	return nil
}

// Get The KafkaChannel Service Associated With The Specified Channel
func (r *Reconciler) getKafkaChannelService(channel *knativekafkav1alpha1.KafkaChannel) (*corev1.Service, error) {

	// Get The KafkaChannel Service Name
	serviceName := kafkautil.AppendKafkaChannelServiceNameSuffix(channel.Name)

	// Get The Service By Namespace / Name
	service := &corev1.Service{}
	service, err := r.serviceLister.Services(channel.Namespace).Get(serviceName)

	// Return The Results
	return service, err
}

// Create KafkaChannel Service Model For The Specified Channel
func (r *Reconciler) newKafkaChannelService(channel *knativekafkav1alpha1.KafkaChannel) *corev1.Service {

	// Get The KafkaChannel Service Name
	serviceName := kafkautil.AppendKafkaChannelServiceNameSuffix(channel.Name)

	// Get The Dispatcher Service Name For The Channel (One Channel Service Per KafkaChannel Instance)
	deploymentName := util.ChannelDeploymentDnsSafeName(r.kafkaSecretName(channel))
	deploymentServiceAddress := fmt.Sprintf("%s.%s.svc.%s", deploymentName, constants.KnativeEventingNamespace, eventingUtils.GetClusterDomainName())

	// Create & Return The Service Model
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       constants.ServiceKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,       // Must Match KafkaChannel For HOST Parsing In Channel Implementation!
			Namespace: channel.Namespace, // Must Match KafkaChannel For HOST Parsing In Channel Implementation!
			Labels: map[string]string{
				KafkaChannelLabel:          channel.Name,
				KafkaChannelChannelLabel:   "true",                     // Allows for identification of KafkaChannels
				K8sAppChannelSelectorLabel: K8sAppChannelSelectorValue, // Prometheus ServiceMonitor (See Helm Chart)
			},
			OwnerReferences: []metav1.OwnerReference{
				util.NewChannelOwnerReference(channel),
			},
		},
		Spec: corev1.ServiceSpec{
			Type:         corev1.ServiceTypeExternalName,
			ExternalName: deploymentServiceAddress,
		},
	}
}

//
// KafkaChannel Kafka Deployment Service
//
// One K8S Service per Kafka Authorization, in the same knative-eventing namespace,
// referring to the single matching Channel Deployment/Pods (as reconciled below).
//

// Reconcile The Kafka Deployment Service
func (r *Reconciler) reconcileChannelDeploymentService(channel *knativekafkav1alpha1.KafkaChannel) error {

	// Attempt To Get The Deployment Service Associated With The Specified Channel
	service, err := r.getChannelDeploymentService(channel)
	if err != nil {

		// If The Service Was Not Found - Then Create A New One For The Channel
		if errors.IsNotFound(err) {

			// Then Create The New Deployment Service
			r.Logger.Info("Channel Deployment Service Not Found - Creating New One")
			service = r.newChannelDeploymentService(channel)
			service, err = r.KubeClientSet.CoreV1().Services(service.Namespace).Create(service)
			if err != nil {
				r.Logger.Error("Failed To Create Channel Deployment Service", zap.Error(err))
				channel.Status.MarkChannelDeploymentServiceFailed("ChannelDeploymentServiceFailed", fmt.Sprintf("Channel Deployment Service Failed: %s", err))
				return err
			} else {
				r.Logger.Info("Successfully Created Channel Deployment Service")
				// Continue To Update Channel Status
			}

		} else {

			// Failed In Attempt To Get Deployment Service From K8S
			r.Logger.Error("Failed To Get Channel Deployment Service", zap.Error(err))
			channel.Status.MarkChannelDeploymentServiceFailed("ChannelDeploymentServiceFailed", fmt.Sprintf("Channel Deployment Service Failed: %s", err))
			return err
		}
	} else {
		r.Logger.Info("Successfully Verified Channel Deployment Service")
		// Continue To Update Channel Status
	}

	// Update Channel Status
	channel.Status.MarkChannelDeploymentServiceTrue()

	// Return Success
	return nil
}

// Get The Kafka Deployment Service Associated With The Specified Channel
func (r *Reconciler) getChannelDeploymentService(channel *knativekafkav1alpha1.KafkaChannel) (*corev1.Service, error) {

	// Get The Dispatcher Deployment Name For The Channel - Use Same For Service
	deploymentName := util.ChannelDeploymentDnsSafeName(r.kafkaSecretName(channel))

	// Get The Service By Namespace / Name
	service := &corev1.Service{}
	service, err := r.serviceLister.Services(constants.KnativeEventingNamespace).Get(deploymentName)

	// Return The Results
	return service, err
}

// Create Kafka Deployment Service Model For The Specified Channel
func (r *Reconciler) newChannelDeploymentService(channel *knativekafkav1alpha1.KafkaChannel) *corev1.Service {

	// Get The Dispatcher Deployment Name For The Channel - Use Same For Service
	deploymentName := util.ChannelDeploymentDnsSafeName(r.kafkaSecretName(channel))

	// Create & Return The Service Model
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       constants.ServiceKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: constants.KnativeEventingNamespace,
			Labels: map[string]string{
				KafkaChannelChannelLabel:   "true",                     // Allows for identification of KafkaChannels
				K8sAppChannelSelectorLabel: K8sAppChannelSelectorValue, // Prometheus ServiceMonitor (See Helm Chart)
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       constants.HttpPortName,
					Port:       constants.HttpServicePortNumber,
					TargetPort: intstr.FromInt(constants.HttpContainerPortNumber),
				},
				{
					Name:       constants.MetricsPortName,
					Port:       int32(r.environment.MetricsPort),
					TargetPort: intstr.FromInt(r.environment.MetricsPort),
				},
			},
			Selector: map[string]string{
				AppLabel: deploymentName, // Matches Deployment Label Key/Value
			},
		},
	}
}

//
// KafkaChannel Deployment - The Kafka Producer Implementation
//
// One K8S Deployment per Kafka Authorization, in the knative-eventing namespace.
//

// Reconcile The KafkaChannel Deployment
func (r *Reconciler) reconcileChannelDeployment(channel *knativekafkav1alpha1.KafkaChannel) error {

	// Attempt To Get The KafkaChannel Deployment Associated With The Specified Channel
	deployment, err := r.getChannelDeployment(channel)
	if err != nil {

		// If The KafkaChannel Deployment Was Not Found - Then Create A New Deployment For The Channel
		if errors.IsNotFound(err) {

			// Then Create The New Deployment
			r.Logger.Info("KafkaChannel Deployment Not Found - Creating New One")
			deployment, err = r.newChannelDeployment(channel)
			if err != nil {
				r.Logger.Error("Failed To Create KafkaChannel Deployment YAML", zap.Error(err))
				channel.Status.MarkChannelDeploymentFailed("ChannelDeploymentFailed", fmt.Sprintf("Channel Deployment Failed: %s", err))
				return err
			} else {
				deployment, err = r.KubeClientSet.AppsV1().Deployments(deployment.Namespace).Create(deployment)
				if err != nil {
					r.Logger.Error("Failed To Create KafkaChannel Deployment", zap.Error(err))
					channel.Status.MarkChannelDeploymentFailed("ChannelDeploymentFailed", fmt.Sprintf("Channel Deployment Failed: %s", err))
					return err
				} else {
					r.Logger.Info("Successfully Created KafkaChannel Deployment")
					// Continue To Update Channel Status
				}
			}

		} else {

			// Failed In Attempt To Get Deployment From K8S
			r.Logger.Error("Failed To Get KafkaChannel Deployment", zap.Error(err))
			channel.Status.MarkChannelDeploymentFailed("ChannelDeploymentFailed", fmt.Sprintf("Channel Deployment Failed: %s", err))
			return err
		}
	} else {
		r.Logger.Info("Successfully Verified Channel Deployment")
		// Continue To Update Channel Status
	}

	// Update Channel Status
	channel.Status.MarkChannelDeploymentTrue()

	// Return Success
	return nil
}

// Get The KafkaChannel Deployment Associated With The Specified Channel
func (r *Reconciler) getChannelDeployment(channel *knativekafkav1alpha1.KafkaChannel) (*appsv1.Deployment, error) {

	// Get The Channel Deployment Name (One Channel Deployment Per Kafka Auth Secret)
	deploymentName := util.ChannelDeploymentDnsSafeName(r.kafkaSecretName(channel))

	// Get The Channel Deployment By Namespace / Name
	deployment := &appsv1.Deployment{}
	deployment, err := r.deploymentLister.Deployments(constants.KnativeEventingNamespace).Get(deploymentName)

	// Return The Results
	return deployment, err
}

// Create KafkaChannel Deployment Model For The Specified Channel
func (r *Reconciler) newChannelDeployment(channel *knativekafkav1alpha1.KafkaChannel) (*appsv1.Deployment, error) {

	// Get The Channel Deployment Name (One Channel Deployment Per Kafka Auth Secret)
	deploymentName := util.ChannelDeploymentDnsSafeName(r.kafkaSecretName(channel))

	// Replicas Int Value For De-Referencing
	replicas := int32(r.environment.ChannelReplicas)

	// Create The Channel Container Environment Variables
	channelEnvVars, err := r.channelDeploymentEnvVars(channel)
	if err != nil {
		r.Logger.Error("Failed To Create Channel Deployment Environment Variables", zap.Error(err))
		return nil, err
	}

	// Create & Return The Channel's Deployment
	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: appsv1.SchemeGroupVersion.String(),
			Kind:       constants.DeploymentKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: constants.KnativeEventingNamespace,
			Labels: map[string]string{
				AppLabel:                 deploymentName, // Matches Service Selector Key/Value Below
				KafkaChannelChannelLabel: "true",         // Allows for identification of KafkaChannels
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					AppLabel: deploymentName, // Matches Template ObjectMeta Pods
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						AppLabel: deploymentName, // Matched By Deployment Selector Above
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: r.environment.ServiceAccount,
					Containers: []corev1.Container{
						{
							Name:  deploymentName,
							LivenessProbe: &corev1.Probe{
								Handler: corev1.Handler{
									HTTPGet: &corev1.HTTPGetAction{
										Port: intstr.FromInt(constants.HealthPort),
										Path: health.LivenessPath,
									},
								},
								InitialDelaySeconds: constants.ChannelLivenessDelay,
								PeriodSeconds: constants.ChannelLivenessPeriod,
							},
							ReadinessProbe: &corev1.Probe{
								Handler: corev1.Handler{
									HTTPGet: &corev1.HTTPGetAction{
										Port: intstr.FromInt(constants.HealthPort),
										Path: health.ReadinessPath,
									},
								},
								InitialDelaySeconds: constants.ChannelReadinessDelay,
								PeriodSeconds: constants.ChannelReadinessPeriod,
							},
							Image: r.environment.ChannelImage,
							Ports: []corev1.ContainerPort{
								{
									Name:          "server",
									ContainerPort: int32(constants.HttpContainerPortNumber),
								},
							},
							Env:             channelEnvVars,
							ImagePullPolicy: corev1.PullAlways,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      constants.LoggingConfigVolumeName,
									MountPath: constants.LoggingConfigMountPath,
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    r.environment.ChannelCpuRequest,
									corev1.ResourceMemory: r.environment.ChannelMemoryRequest,
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    r.environment.ChannelCpuLimit,
									corev1.ResourceMemory: r.environment.ChannelMemoryLimit,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: constants.LoggingConfigVolumeName,
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: constants.LoggingConfigMapName,
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Return Channel Deployment
	return deployment, nil
}

// Create The Channel Container's Env Vars
func (r *Reconciler) channelDeploymentEnvVars(channel *knativekafkav1alpha1.KafkaChannel) ([]corev1.EnvVar, error) {

	// Get The TopicName For Specified Channel
	topicName := util.TopicName(channel)

	// Create The Channel Deployment EnvVars
	envVars := []corev1.EnvVar{
		{
			Name:  env.MetricsPortEnvVarKey,
			Value: strconv.Itoa(r.environment.MetricsPort),
		},
		{
			Name:  env.HealthPortEnvVarKey,
			Value: strconv.Itoa(constants.HealthPort),
		},
	}

	// Get The Kafka Secret From The Kafka Admin Client
	kafkaSecret := r.adminClient.GetKafkaSecretName(topicName)

	// If The Kafka Secret Env Var Is Specified Then Append Relevant Env Vars
	if len(kafkaSecret) <= 0 {

		// Received Invalid Kafka Secret - Cannot Proceed
		return nil, fmt.Errorf("invalid kafkaSecret for topic '%s'", topicName)

	} else {

		// Append The Kafka Brokers As Env Var
		envVars = append(envVars, corev1.EnvVar{
			Name: env.KafkaBrokerEnvVarKey,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: kafkaSecret},
					Key:                  constants.KafkaSecretDataKeyBrokers,
				},
			},
		})

		// Append The Kafka Username As Env Var
		envVars = append(envVars, corev1.EnvVar{
			Name: env.KafkaUsernameEnvVarKey,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: kafkaSecret},
					Key:                  constants.KafkaSecretDataKeyUsername,
				},
			},
		})

		// Append The Kafka Password As Env Var
		envVars = append(envVars, corev1.EnvVar{
			Name: env.KafkaPasswordEnvVarKey,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: kafkaSecret},
					Key:                  constants.KafkaSecretDataKeyPassword,
				},
			},
		})
	}

	// Return The Channel Deployment EnvVars Array
	return envVars, nil
}

// Get The Kafka Auth Secret Corresponding To The Specified KafkaChannel
func (r *Reconciler) kafkaSecretName(channel *knativekafkav1alpha1.KafkaChannel) string {
	return r.adminClient.GetKafkaSecretName(util.TopicName(channel))
}
