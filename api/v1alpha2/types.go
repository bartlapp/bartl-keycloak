package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "keycloak.bartl.sh", Version: "v1alpha2"}

func AddToScheme(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &KeycloakApplication{}, &KeycloakApplicationList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}

type Identity struct {
	Installation string `json:"installation"`
	Instance     string `json:"instance"`
}
type Application struct {
	Replicas int32 `json:"replicas"`
}
type Configuration struct {
	Hostname string `json:"hostname"`
}
type Database struct {
	SecretName      string `json:"secretName"`
	AdminSecretName string `json:"adminSecretName"`
	Size            string `json:"size"`
}
type Ingress struct {
	Enabled                   bool              `json:"enabled"`
	ClassName                 string            `json:"className,omitempty"`
	TLSSecretName             string            `json:"tlsSecretName,omitempty"`
	Maintenance               bool              `json:"maintenance"`
	AdministratorSourceRanges []string          `json:"administratorSourceRanges,omitempty"`
	ControllerNamespace       string            `json:"controllerNamespace,omitempty"`
	ControllerPodLabels       map[string]string `json:"controllerPodLabels,omitempty"`
}
type Backup struct {
	Repository   string `json:"repository"`
	ReaderSecret string `json:"readerSecret"`
	WriterSecret string `json:"writerSecret"`
	Schedule     string `json:"schedule"`
	TimeZone     string `json:"timeZone,omitempty"`
	Suspend      bool   `json:"suspend"`
}
type Spec struct {
	Running              bool          `json:"running"`
	Identity             Identity      `json:"identity"`
	PackageRelease       string        `json:"packageRelease"`
	Application          Application   `json:"application"`
	Configuration        Configuration `json:"configuration"`
	Database             Database      `json:"database"`
	BootstrapAdminSecret string        `json:"bootstrapAdminSecret,omitempty"`
	Ingress              Ingress       `json:"ingress"`
	Backup               *Backup       `json:"backup,omitempty"`
}
type State struct {
	Classification string `json:"classification"`
	Reason         string `json:"reason"`
}
type ApplicationStatus struct {
	DesiredReplicas int32 `json:"desiredReplicas"`
	ReadyReplicas   int32 `json:"readyReplicas"`
	ActivePods      int32 `json:"activePods"`
}
type OperationTarget struct {
	PVCUID           string `json:"pvcUID"`
	DatabaseIdentity string `json:"databaseIdentity"`
}
type OperationSnapshot struct {
	RepositoryIdentity string `json:"repositoryIdentity"`
	ID                 string `json:"id"`
}
type OperationJob struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}
type Operation struct {
	ID                  string            `json:"id"`
	Kind                string            `json:"kind"`
	InitiatedGeneration int64             `json:"initiatedGeneration"`
	Target              OperationTarget   `json:"target"`
	Snapshot            OperationSnapshot `json:"snapshot"`
	Job                 OperationJob      `json:"job"`
	Result              string            `json:"result"`
}
type Status struct {
	Replicas           int32              `json:"replicas"`
	Selector           string             `json:"selector"`
	ObservedGeneration int64              `json:"observedGeneration"`
	PackageRelease     string             `json:"packageRelease"`
	State              State              `json:"state"`
	Application        ApplicationStatus  `json:"application"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	Operation          *Operation         `json:"operation,omitempty"`
}
type KeycloakApplication struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              Spec   `json:"spec"`
	Status            Status `json:"status,omitempty"`
}
type KeycloakApplicationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KeycloakApplication `json:"items"`
}

func (a *KeycloakApplication) DeepCopyObject() runtime.Object {
	if a == nil {
		return (*KeycloakApplication)(nil)
	}
	b := new(KeycloakApplication)
	*b = *a
	a.ObjectMeta.DeepCopyInto(&b.ObjectMeta)
	b.Spec.Ingress.AdministratorSourceRanges = append([]string(nil), a.Spec.Ingress.AdministratorSourceRanges...)
	if a.Spec.Ingress.ControllerPodLabels != nil {
		b.Spec.Ingress.ControllerPodLabels = make(map[string]string, len(a.Spec.Ingress.ControllerPodLabels))
		for key, value := range a.Spec.Ingress.ControllerPodLabels {
			b.Spec.Ingress.ControllerPodLabels[key] = value
		}
	}
	if a.Spec.Backup != nil {
		backup := *a.Spec.Backup
		b.Spec.Backup = &backup
	}
	b.Status.Conditions = append([]metav1.Condition(nil), a.Status.Conditions...)
	if a.Status.Operation != nil {
		operation := *a.Status.Operation
		b.Status.Operation = &operation
	}
	return b
}
func (a *KeycloakApplicationList) DeepCopyObject() runtime.Object {
	if a == nil {
		return (*KeycloakApplicationList)(nil)
	}
	b := new(KeycloakApplicationList)
	*b = *a
	a.ListMeta.DeepCopyInto(&b.ListMeta)
	if a.Items != nil {
		b.Items = make([]KeycloakApplication, len(a.Items))
		for i := range a.Items {
			b.Items[i] = *a.Items[i].DeepCopyObject().(*KeycloakApplication)
		}
	}
	return b
}
