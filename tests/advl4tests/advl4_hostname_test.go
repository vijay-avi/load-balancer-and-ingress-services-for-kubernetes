package advl4tests

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/vmware/load-balancer-and-ingress-services-for-kubernetes/internal/cache"
	"github.com/vmware/load-balancer-and-ingress-services-for-kubernetes/internal/k8s"
	"github.com/vmware/load-balancer-and-ingress-services-for-kubernetes/internal/lib"
	avinodes "github.com/vmware/load-balancer-and-ingress-services-for-kubernetes/internal/nodes"
	"github.com/vmware/load-balancer-and-ingress-services-for-kubernetes/internal/objects"
	"github.com/vmware/load-balancer-and-ingress-services-for-kubernetes/pkg/utils"
	"github.com/vmware/load-balancer-and-ingress-services-for-kubernetes/tests/integrationtest"

	"github.com/onsi/gomega"
	advl4v1alpha1pre1 "github.com/vmware-tanzu/service-apis/apis/v1alpha1pre1"
	advl4fake "github.com/vmware-tanzu/service-apis/pkg/client/clientset/versioned/fake"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

var KubeClient *k8sfake.Clientset
var AdvL4Client *advl4fake.Clientset
var ctrl *k8s.AviController

func TestMain(m *testing.M) {
	os.Setenv("CLUSTER_ID", "abc:cluster")
	os.Setenv("CLOUD_NAME", "Default-Cloud")
	os.Setenv("SEG_NAME", "Default-Group")
	os.Setenv("ADVANCED_L4", "true")

	KubeClient = k8sfake.NewSimpleClientset()
	// CRDClient = crdfake.NewSimpleClientset()
	AdvL4Client = advl4fake.NewSimpleClientset()
	// lib.SetCRDClientset(CRDClient)
	lib.SetAdvL4Clientset(AdvL4Client)

	registeredInformers := []string{
		utils.ServiceInformer,
		utils.EndpointInformer,
		utils.IngressInformer,
		utils.SecretInformer,
		utils.NSInformer,
		utils.NodeInformer,
		utils.ConfigMapInformer,
	}
	utils.NewInformers(utils.KubeClientIntf{KubeClient}, registeredInformers)
	informers := k8s.K8sinformers{Cs: KubeClient}
	// k8s.NewCRDInformers(CRDClient)
	k8s.NewAdvL4Informers(AdvL4Client)

	mcache := cache.SharedAviObjCache()
	cloudObj := &cache.AviCloudPropertyCache{Name: "Default-Cloud", VType: "mock"}
	subdomains := []string{"avi.internal", ".com"}
	cloudObj.NSIpamDNS = subdomains
	mcache.CloudKeyCache.AviCacheAdd("Default-Cloud", cloudObj)

	integrationtest.InitializeFakeAKOAPIServer()

	integrationtest.NewAviFakeClientInstance()
	defer integrationtest.AviFakeClientInstance.Close()

	ctrl = k8s.SharedAviController()
	stopCh := utils.SetupSignalHandler()
	ctrlCh := make(chan struct{})
	quickSyncCh := make(chan struct{})
	waitGroupMap := make(map[string]*sync.WaitGroup)
	wgIngestion := &sync.WaitGroup{}
	waitGroupMap["ingestion"] = wgIngestion
	wgFastRetry := &sync.WaitGroup{}
	waitGroupMap["fastretry"] = wgFastRetry
	wgSlowRetry := &sync.WaitGroup{}
	waitGroupMap["slowretry"] = wgSlowRetry
	wgGraph := &sync.WaitGroup{}
	waitGroupMap["graph"] = wgGraph
	ctrl.HandleConfigMap(informers, ctrlCh, stopCh, quickSyncCh)
	go ctrl.InitController(informers, registeredInformers, ctrlCh, stopCh, quickSyncCh, waitGroupMap)
	AddConfigMap()
	integrationtest.KubeClient = KubeClient
	os.Exit(m.Run())
}

func AddConfigMap() {
	aviCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "vmware-system-ako",
			Name:      "avi-k8s-config",
		},
	}
	KubeClient.CoreV1().ConfigMaps("vmware-system-ako").Create(aviCM)
	integrationtest.PollForSyncStart(ctrl, 10)
}

// Gateway/GatewayClass lib functions
type FakeGateway struct {
	Name      string
	Namespace string
	GWClass   string
	Listeners []FakeGWListener
}

type FakeGWListener struct {
	Port     int32
	Protocol string
	Labels   map[string]string
}

func (gw FakeGateway) Gateway() *advl4v1alpha1pre1.Gateway {
	var fakeListeners []advl4v1alpha1pre1.Listener
	for _, listener := range gw.Listeners {
		fakeListeners = append(fakeListeners, advl4v1alpha1pre1.Listener{
			Port:     listener.Port,
			Protocol: advl4v1alpha1pre1.ProtocolType(listener.Protocol),
			Routes: advl4v1alpha1pre1.RouteBindingSelector{
				Resource: "services",
				RouteSelector: metav1.LabelSelector{
					MatchLabels: listener.Labels,
				},
			},
		})
	}

	gateway := &advl4v1alpha1pre1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: gw.Namespace,
			Name:      gw.Name,
		},
		Spec: advl4v1alpha1pre1.GatewaySpec{
			Class:     gw.GWClass,
			Listeners: fakeListeners,
		},
	}

	return gateway
}

func SetupGateway(t *testing.T, gwname, namespace, gwclass string) {
	gateway := FakeGateway{
		Name:      gwname,
		Namespace: namespace,
		GWClass:   gwclass,
		Listeners: []FakeGWListener{{
			Port:     int32(8081),
			Protocol: "TCP",
			Labels: map[string]string{
				lib.GatewayNameLabelKey:      gwname,
				lib.GatewayNamespaceLabelKey: namespace,
				lib.GatewayTypeLabelKey:      "direct",
			},
		}},
	}

	gwCreate := gateway.Gateway()
	if _, err := lib.GetAdvL4Clientset().NetworkingV1alpha1pre1().Gateways(namespace).Create(gwCreate); err != nil {
		t.Fatalf("error in adding Gateway: %v", err)
	}
}

func TeardownGateway(t *testing.T, gwname, namespace string) {
	if err := lib.GetAdvL4Clientset().NetworkingV1alpha1pre1().Gateways(namespace).Delete(gwname, nil); err != nil {
		t.Fatalf("error in deleting Gateway: %v", err)
	}
}

type FakeGWClass struct {
	Name       string
	Controller string
}

func (gwclass FakeGWClass) GatewayClass() *advl4v1alpha1pre1.GatewayClass {
	gatewayclass := &advl4v1alpha1pre1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: gwclass.Name,
		},
		Spec: advl4v1alpha1pre1.GatewayClassSpec{
			Controller: gwclass.Controller,
		},
	}

	return gatewayclass
}

func SetupGatewayClass(t *testing.T, gwclassName, controller string) {
	gatewayclass := FakeGWClass{
		Name:       gwclassName,
		Controller: controller,
	}

	gwClassCreate := gatewayclass.GatewayClass()
	if _, err := lib.GetAdvL4Clientset().NetworkingV1alpha1pre1().GatewayClasses().Create(gwClassCreate); err != nil {
		t.Fatalf("error in adding GatewayClass: %v", err)
	}
}

func TeardownGatewayClass(t *testing.T, gwClassName string) {
	if err := lib.GetAdvL4Clientset().NetworkingV1alpha1pre1().GatewayClasses().Delete(gwClassName, nil); err != nil {
		t.Fatalf("error in deleting GatewayClass: %v", err)
	}
}

func SetupAdvLBService(t *testing.T, svcname, namespace, gwname, gwnamespace string) {
	svc := integrationtest.FakeService{
		Name:      svcname,
		Namespace: namespace,
		Labels: map[string]string{
			lib.GatewayNameLabelKey:      gwname,
			lib.GatewayNamespaceLabelKey: gwnamespace,
			lib.GatewayTypeLabelKey:      "direct",
		},
		Type:         corev1.ServiceTypeLoadBalancer,
		ServicePorts: []integrationtest.Serviceport{{PortName: "foo1", Protocol: "TCP", PortNumber: 8081, TargetPort: 8081}},
	}

	svcCreate := svc.Service()
	if _, err := KubeClient.CoreV1().Services(namespace).Create(svcCreate); err != nil {
		t.Fatalf("error in adding Service: %v", err)
	}
}

func TeardownAdvLBService(t *testing.T, svcname, namespace string) {
	if err := KubeClient.CoreV1().Services(namespace).Delete(svcname, nil); err != nil {
		t.Fatalf("error in deleting AdvLB Service: %v", err)
	}
}

func VerifyGatewayVSNodeDeletion(g *gomega.WithT, modelName string) {
	g.Eventually(func() interface{} {
		_, aviModel := objects.SharedAviGraphLister().Get(modelName)
		return aviModel
	}, 30*time.Second).Should(gomega.BeNil())
}

func TestAdvL4BestCase(t *testing.T) {
	// create gwclass, create gw, create 1svc
	// check graph VsNode vals, check IP status
	// remove gwclasss, IP removed
	g := gomega.NewGomegaWithT(t)

	gwClassName, gatewayName, ns := "avi-lb", "my-gateway", "default"
	modelName := "admin/abc--default-my-gateway"

	SetupGatewayClass(t, gwClassName, lib.AviGatewayController)
	SetupGateway(t, gatewayName, ns, gwClassName)

	SetupAdvLBService(t, "svc", ns, gatewayName, ns)

	g.Eventually(func() string {
		gw, _ := AdvL4Client.NetworkingV1alpha1pre1().Gateways(ns).Get(gatewayName, metav1.GetOptions{})
		if len(gw.Status.Addresses) > 0 {
			return gw.Status.Addresses[0].Value
		}
		return ""
	}, 40*time.Second).Should(gomega.Equal("10.250.250.250"))
	svc, _ := KubeClient.CoreV1().Services(ns).Get("svc", metav1.GetOptions{})
	g.Expect(svc.Status.LoadBalancer.Ingress[0].IP).To(gomega.Equal("10.250.250.250"))

	_, aviModel := objects.SharedAviGraphLister().Get(modelName)
	nodes := aviModel.(*avinodes.AviObjectGraph).GetAviVS()
	g.Expect(nodes[0].PortProto[0].Port).To(gomega.Equal(int32(8081)))
	g.Expect(nodes[0].HttpPolicySetRefs).To(gomega.HaveLen(0))
	g.Expect(nodes[0].L4PolicyRefs).To(gomega.HaveLen(1))
	g.Expect(nodes[0].L4PolicyRefs[0].PortPool[0].Port).To(gomega.Equal(uint32(8081)))
	g.Expect(nodes[0].L4PolicyRefs[0].PortPool[0].Protocol).To(gomega.Equal("TCP"))
	g.Expect(nodes[0].ServiceMetadata.NamespaceServiceName[0]).To(gomega.Equal("default/svc"))
	g.Expect(nodes[0].ServiceMetadata.Gateway).To(gomega.Equal("default/my-gateway"))

	TeardownGatewayClass(t, gwClassName)
	g.Eventually(func() int {
		gw, _ := AdvL4Client.NetworkingV1alpha1pre1().Gateways(ns).Get(gatewayName, metav1.GetOptions{})
		return len(gw.Status.Addresses)
	}, 10*time.Second).Should(gomega.Equal(0))

	TeardownAdvLBService(t, "svc", ns)
	TeardownGateway(t, gatewayName, ns)
	VerifyGatewayVSNodeDeletion(g, modelName)
}

// Gateway - GWClass mapping tests

func TestAdvL4WrongControllerGWClass(t *testing.T) {
	// create gateway, nothing happens
	// create gatewayclass, VS created
	// update to bad gatewayclass (wrong controller), VS deleted
	g := gomega.NewGomegaWithT(t)

	gwClassName, gatewayName, ns := "avi-lb", "my-gateway", "default"
	modelName := "admin/abc--default-my-gateway"

	SetupGateway(t, gatewayName, ns, gwClassName)
	SetupAdvLBService(t, "svc", ns, gatewayName, ns)

	SetupGatewayClass(t, gwClassName, lib.AviGatewayController)

	g.Eventually(func() string {
		gw, _ := AdvL4Client.NetworkingV1alpha1pre1().Gateways(ns).Get(gatewayName, metav1.GetOptions{})
		if len(gw.Status.Addresses) > 0 {
			return gw.Status.Addresses[0].Value
		}
		return ""
	}, 10*time.Second).Should(gomega.Equal("10.250.250.250"))

	gwclassUpdate := FakeGWClass{
		Name:       gwClassName,
		Controller: "xyz",
	}.GatewayClass()
	gwclassUpdate.ResourceVersion = "2"
	if _, err := lib.GetAdvL4Clientset().NetworkingV1alpha1pre1().GatewayClasses().Update(gwclassUpdate); err != nil {
		t.Fatalf("error in updating GatewayClass: %v", err)
	}

	g.Eventually(func() int {
		gw, _ := AdvL4Client.NetworkingV1alpha1pre1().Gateways(ns).Get(gatewayName, metav1.GetOptions{})
		return len(gw.Status.Addresses)
	}, 10*time.Second).Should(gomega.Equal(0))
	svc, _ := KubeClient.CoreV1().Services(ns).Get("svc", metav1.GetOptions{})
	g.Expect(svc.Status.LoadBalancer.Ingress).To(gomega.HaveLen(0))

	TeardownAdvLBService(t, "svc", ns)
	TeardownGateway(t, gatewayName, ns)
	TeardownGatewayClass(t, gwClassName)
	VerifyGatewayVSNodeDeletion(g, modelName)
}

func TestAdvL4WrongClassMappingInGateway(t *testing.T) {
	// create gwclass, gw
	// update wrong mapping of class in gw, VS deleted
	// fix class in gw, VS created
	g := gomega.NewGomegaWithT(t)

	gwClassName, gatewayName, ns := "avi-lb", "my-gateway", "default"
	modelName := "admin/abc--default-my-gateway"

	SetupGateway(t, gatewayName, ns, gwClassName)
	SetupAdvLBService(t, "svc", ns, gatewayName, ns)

	SetupGatewayClass(t, gwClassName, lib.AviGatewayController)

	g.Eventually(func() string {
		gw, _ := AdvL4Client.NetworkingV1alpha1pre1().Gateways(ns).Get(gatewayName, metav1.GetOptions{})
		if len(gw.Status.Addresses) > 0 {
			return gw.Status.Addresses[0].Value
		}
		return ""
	}, 10*time.Second).Should(gomega.Equal("10.250.250.250"))

	gwUpdate := FakeGateway{
		Name: gatewayName, Namespace: ns, GWClass: gwClassName,
		Listeners: []FakeGWListener{{
			Port: int32(8081), Protocol: "TCP",
			Labels: map[string]string{
				lib.GatewayNameLabelKey:      "BADGATEWAY",
				lib.GatewayNamespaceLabelKey: ns,
				lib.GatewayTypeLabelKey:      "direct",
			},
		}},
	}.Gateway()
	gwUpdate.ResourceVersion = "2"
	if _, err := lib.GetAdvL4Clientset().NetworkingV1alpha1pre1().Gateways(ns).Update(gwUpdate); err != nil {
		t.Fatalf("error in updating Gateway: %v", err)
	}

	// vsNode must get deleted
	VerifyGatewayVSNodeDeletion(g, modelName)
	g.Eventually(func() int {
		gw, _ := AdvL4Client.NetworkingV1alpha1pre1().Gateways(ns).Get(gatewayName, metav1.GetOptions{})
		return len(gw.Status.Addresses)
	}, 10*time.Second).Should(gomega.Equal(0))

	gwUpdate = FakeGateway{
		Name: gatewayName, Namespace: ns, GWClass: gwClassName,
		Listeners: []FakeGWListener{{
			Port: int32(8081), Protocol: "TCP",
			Labels: map[string]string{
				lib.GatewayNameLabelKey:      gatewayName,
				lib.GatewayNamespaceLabelKey: ns,
				lib.GatewayTypeLabelKey:      "direct",
			},
		}},
	}.Gateway()
	gwUpdate.ResourceVersion = "3"
	if _, err := lib.GetAdvL4Clientset().NetworkingV1alpha1pre1().Gateways(ns).Update(gwUpdate); err != nil {
		t.Fatalf("error in updating Gateway: %v", err)
	}

	// vsNode must come back up
	g.Eventually(func() int {
		gw, _ := AdvL4Client.NetworkingV1alpha1pre1().Gateways(ns).Get(gatewayName, metav1.GetOptions{})
		return len(gw.Status.Addresses)
	}, 10*time.Second).Should(gomega.Equal(1))

	TeardownAdvLBService(t, "svc", ns)
	TeardownGateway(t, gatewayName, ns)
	TeardownGatewayClass(t, gwClassName)
	VerifyGatewayVSNodeDeletion(g, modelName)
}

func TestAdvL4ProtocolChangeInService(t *testing.T) {
	// gw/tcp/8081 svc/tcp/8081  -> svc/udp/8081
	// service protocol changes VS deleted
	g := gomega.NewGomegaWithT(t)

	gwClassName, gatewayName, ns := "avi-lb", "my-gateway", "default"
	modelName := "admin/abc--default-my-gateway"

	SetupGatewayClass(t, gwClassName, lib.AviGatewayController)
	SetupGateway(t, gatewayName, ns, gwClassName)
	SetupAdvLBService(t, "svc", ns, gatewayName, ns)

	g.Eventually(func() bool {
		if found, aviModel := objects.SharedAviGraphLister().Get(modelName); found && aviModel != nil {
			nodes := aviModel.(*avinodes.AviObjectGraph).GetAviVS()
			if len(nodes) > 0 && len(nodes[0].PoolRefs) == 1 {
				return true
			}
		}
		return false
	}, 10*time.Second).Should(gomega.Equal(true))

	svcUpdate := integrationtest.FakeService{
		Name:      "svc",
		Namespace: ns,
		Labels: map[string]string{
			lib.GatewayNameLabelKey:      gatewayName,
			lib.GatewayNamespaceLabelKey: ns,
			lib.GatewayTypeLabelKey:      "direct",
		},
		Type:         corev1.ServiceTypeLoadBalancer,
		ServicePorts: []integrationtest.Serviceport{{PortName: "foo1", Protocol: corev1.ProtocolUDP, PortNumber: 8081, TargetPort: 8081}},
	}.Service()
	fmt.Println(utils.Stringify(svcUpdate))
	svcUpdate.ResourceVersion = "2"
	if _, err := KubeClient.CoreV1().Services(ns).Update(svcUpdate); err != nil {
		t.Fatalf("error in updating Service: %v", err)
	}

	g.Eventually(func() bool {
		found, aviModel := objects.SharedAviGraphLister().Get(modelName)
		if found && aviModel == nil {
			return false
		}
		return true
	}, 20*time.Second).Should(gomega.Equal(false))

	TeardownAdvLBService(t, "svc", ns)
	TeardownGateway(t, gatewayName, ns)
	TeardownGatewayClass(t, gwClassName)
	VerifyGatewayVSNodeDeletion(g, modelName)
}

func TestAdvL4PortChangeInService(t *testing.T) {
	// gw/tcp/8081 svc/tcp/8081 -> svc/tcp/8080
	// service port changes VS deleted
	g := gomega.NewGomegaWithT(t)

	gwClassName, gatewayName, ns := "avi-lb", "my-gateway", "default"
	modelName := "admin/abc--default-my-gateway"

	SetupGatewayClass(t, gwClassName, lib.AviGatewayController)
	SetupGateway(t, gatewayName, ns, gwClassName)
	SetupAdvLBService(t, "svc", ns, gatewayName, ns)

	g.Eventually(func() bool {
		if found, aviModel := objects.SharedAviGraphLister().Get(modelName); found && aviModel != nil {
			nodes := aviModel.(*avinodes.AviObjectGraph).GetAviVS()
			if len(nodes) > 0 && len(nodes[0].PoolRefs) == 1 {
				return true
			}
		}
		return false
	}, 10*time.Second).Should(gomega.Equal(true))

	svcUpdate := integrationtest.FakeService{
		Name:      "svc",
		Namespace: ns,
		Labels: map[string]string{
			lib.GatewayNameLabelKey:      gatewayName,
			lib.GatewayNamespaceLabelKey: ns,
			lib.GatewayTypeLabelKey:      "direct",
		},
		Type:         corev1.ServiceTypeLoadBalancer,
		ServicePorts: []integrationtest.Serviceport{{PortName: "foo1", Protocol: corev1.ProtocolTCP, PortNumber: 8080, TargetPort: 8081}},
	}.Service()
	fmt.Println(utils.Stringify(svcUpdate))
	svcUpdate.ResourceVersion = "2"
	if _, err := KubeClient.CoreV1().Services(ns).Update(svcUpdate); err != nil {
		t.Fatalf("error in updating Service: %v", err)
	}

	g.Eventually(func() bool {
		found, aviModel := objects.SharedAviGraphLister().Get(modelName)
		if found && aviModel == nil {
			return false
		}
		return true
	}, 20*time.Second).Should(gomega.Equal(false))

	TeardownAdvLBService(t, "svc", ns)
	TeardownGateway(t, gatewayName, ns)
	TeardownGatewayClass(t, gwClassName)
	VerifyGatewayVSNodeDeletion(g, modelName)
}

func TestAdvL4LabelUpdatesInService(t *testing.T) {
	// correct labels, label mismatch, correct labels, delete labels
	g := gomega.NewGomegaWithT(t)

	gwClassName, gatewayName, ns := "avi-lb", "my-gateway", "default"
	modelName := "admin/abc--default-my-gateway"

	SetupGatewayClass(t, gwClassName, lib.AviGatewayController)
	SetupGateway(t, gatewayName, ns, gwClassName)
	SetupAdvLBService(t, "svc", ns, gatewayName, ns)

	g.Eventually(func() bool {
		if found, aviModel := objects.SharedAviGraphLister().Get(modelName); found && aviModel != nil {
			nodes := aviModel.(*avinodes.AviObjectGraph).GetAviVS()
			if len(nodes) > 0 && len(nodes[0].PoolRefs) == 1 {
				return true
			}
		}
		return false
	}, 10*time.Second).Should(gomega.Equal(true))

	svcUpdate := integrationtest.FakeService{
		Name:      "svc",
		Namespace: ns,
		Labels: map[string]string{
			lib.GatewayNameLabelKey:      "BADGATEWAY",
			lib.GatewayNamespaceLabelKey: ns,
			lib.GatewayTypeLabelKey:      "direct",
		},
		Type:         corev1.ServiceTypeLoadBalancer,
		ServicePorts: []integrationtest.Serviceport{{PortName: "foo1", Protocol: corev1.ProtocolTCP, PortNumber: 8081, TargetPort: 8081}},
	}.Service()
	fmt.Println(utils.Stringify(svcUpdate))
	svcUpdate.ResourceVersion = "2"
	if _, err := KubeClient.CoreV1().Services(ns).Update(svcUpdate); err != nil {
		t.Fatalf("error in updating Service: %v", err)
	}

	g.Eventually(func() bool {
		found, aviModel := objects.SharedAviGraphLister().Get(modelName)
		if found && aviModel == nil {
			return false
		}
		return true
	}, 20*time.Second).Should(gomega.Equal(false))

	TeardownAdvLBService(t, "svc", ns)
	TeardownGateway(t, gatewayName, ns)
	TeardownGatewayClass(t, gwClassName)
	VerifyGatewayVSNodeDeletion(g, modelName)
}

func TestAdvL4LabelUpdatesInGateway(t *testing.T) {
	// correct labels, label mismatch, correct labels, delete labels
	g := gomega.NewGomegaWithT(t)

	gwClassName, gatewayName, ns := "avi-lb", "my-gateway", "default"
	modelName := "admin/abc--default-my-gateway"

	SetupGatewayClass(t, gwClassName, lib.AviGatewayController)
	SetupGateway(t, gatewayName, ns, gwClassName)
	SetupAdvLBService(t, "svc", ns, gatewayName, ns)

	g.Eventually(func() bool {
		if found, aviModel := objects.SharedAviGraphLister().Get(modelName); found && aviModel != nil {
			nodes := aviModel.(*avinodes.AviObjectGraph).GetAviVS()
			if len(nodes) > 0 && len(nodes[0].PoolRefs) == 1 {
				return true
			}
		}
		return false
	}, 10*time.Second).Should(gomega.Equal(true))

	gwUpdate := FakeGateway{
		Name:      gatewayName,
		Namespace: ns,
		GWClass:   gwClassName,
		Listeners: []FakeGWListener{{
			Port:     int32(8081),
			Protocol: "TCP",
			Labels: map[string]string{
				lib.GatewayNameLabelKey:      "BADGATEWAY",
				lib.GatewayNamespaceLabelKey: ns,
				lib.GatewayTypeLabelKey:      "direct",
			},
		}},
	}.Gateway()
	gwUpdate.ResourceVersion = "2"
	if _, err := lib.GetAdvL4Clientset().NetworkingV1alpha1pre1().Gateways(ns).Update(gwUpdate); err != nil {
		t.Fatalf("error in updating Gateway: %v", err)
	}

	g.Eventually(func() bool {
		found, aviModel := objects.SharedAviGraphLister().Get(modelName)
		if found && aviModel == nil {
			return false
		}
		return true
	}, 20*time.Second).Should(gomega.Equal(false))

	TeardownAdvLBService(t, "svc", ns)
	TeardownGateway(t, gatewayName, ns)
	TeardownGatewayClass(t, gwClassName)
	VerifyGatewayVSNodeDeletion(g, modelName)
}

func TestAdvL4GatewayListenerPortUpdate(t *testing.T) {
	// svc/tcp/8081
	// change gateway listener port to 8080, VS deletes
	// change svc port to 8080, VS creates, with 8080 exposed port
	g := gomega.NewGomegaWithT(t)

	gwClassName, gatewayName, ns := "avi-lb", "my-gateway", "default"
	modelName := "admin/abc--default-my-gateway"

	SetupGatewayClass(t, gwClassName, lib.AviGatewayController)
	SetupGateway(t, gatewayName, ns, gwClassName)
	SetupAdvLBService(t, "svc", ns, gatewayName, ns)

	g.Eventually(func() bool {
		if found, aviModel := objects.SharedAviGraphLister().Get(modelName); found && aviModel != nil {
			nodes := aviModel.(*avinodes.AviObjectGraph).GetAviVS()
			if len(nodes) > 0 &&
				len(nodes[0].PoolRefs) == 1 &&
				nodes[0].PoolRefs[0].Protocol == "TCP" {
				return true
			}
		}
		return false
	}, 10*time.Second).Should(gomega.Equal(true))

	gwUpdate := FakeGateway{
		Name:      gatewayName,
		Namespace: ns,
		GWClass:   gwClassName,
		Listeners: []FakeGWListener{{
			Port:     int32(8080),
			Protocol: "TCP",
			Labels: map[string]string{
				lib.GatewayNameLabelKey:      gatewayName,
				lib.GatewayNamespaceLabelKey: ns,
				lib.GatewayTypeLabelKey:      "direct",
			},
		}},
	}.Gateway()
	gwUpdate.ResourceVersion = "2"
	if _, err := lib.GetAdvL4Clientset().NetworkingV1alpha1pre1().Gateways(ns).Update(gwUpdate); err != nil {
		t.Fatalf("error in updating Gateway: %v", err)
	}

	g.Eventually(func() bool {
		found, aviModel := objects.SharedAviGraphLister().Get(modelName)
		if found && aviModel == nil {
			return false
		}
		return true
	}, 20*time.Second).Should(gomega.Equal(false))

	// match service to chaged gateway port: 8080
	svcUpdate := integrationtest.FakeService{
		Name:      "svc",
		Namespace: ns,
		Labels: map[string]string{
			lib.GatewayNameLabelKey:      gatewayName,
			lib.GatewayNamespaceLabelKey: ns,
			lib.GatewayTypeLabelKey:      "direct",
		},
		Type:         corev1.ServiceTypeLoadBalancer,
		ServicePorts: []integrationtest.Serviceport{{PortName: "foo1", Protocol: corev1.ProtocolTCP, PortNumber: 8080, TargetPort: 8081}},
	}.Service()
	fmt.Println(utils.Stringify(svcUpdate))
	svcUpdate.ResourceVersion = "2"
	if _, err := KubeClient.CoreV1().Services(ns).Update(svcUpdate); err != nil {
		t.Fatalf("error in updating Service: %v", err)
	}

	g.Eventually(func() bool {
		if found, aviModel := objects.SharedAviGraphLister().Get(modelName); found && aviModel != nil {
			nodes := aviModel.(*avinodes.AviObjectGraph).GetAviVS()
			if len(nodes) > 0 &&
				len(nodes[0].PoolRefs) == 1 &&
				nodes[0].PoolRefs[0].Protocol == "TCP" {
				return true
			}
		}
		return false
	}, 10*time.Second).Should(gomega.Equal(true))

	TeardownAdvLBService(t, "svc", ns)
	TeardownGateway(t, gatewayName, ns)
	TeardownGatewayClass(t, gwClassName)
	VerifyGatewayVSNodeDeletion(g, modelName)
}

func TestAdvL4GatewayListenerProtocolUpdate(t *testing.T) {
	// svc/tcp/8080
	// change gateway listener protocol to UDP, VS deletes
	// change svc protocol to UDP, VS creates, with UDP protocol
	g := gomega.NewGomegaWithT(t)

	gwClassName, gatewayName, ns := "avi-lb", "my-gateway", "default"
	modelName := "admin/abc--default-my-gateway"

	SetupGatewayClass(t, gwClassName, lib.AviGatewayController)
	SetupGateway(t, gatewayName, ns, gwClassName)
	SetupAdvLBService(t, "svc", ns, gatewayName, ns)

	g.Eventually(func() bool {
		if found, aviModel := objects.SharedAviGraphLister().Get(modelName); found && aviModel != nil {
			nodes := aviModel.(*avinodes.AviObjectGraph).GetAviVS()
			if len(nodes) > 0 &&
				len(nodes[0].PoolRefs) == 1 &&
				nodes[0].PoolRefs[0].Protocol == "TCP" {
				return true
			}
		}
		return false
	}, 10*time.Second).Should(gomega.Equal(true))

	gwUpdate := FakeGateway{
		Name:      gatewayName,
		Namespace: ns,
		GWClass:   gwClassName,
		Listeners: []FakeGWListener{{
			Port:     int32(8081),
			Protocol: "UDP",
			Labels: map[string]string{
				lib.GatewayNameLabelKey:      gatewayName,
				lib.GatewayNamespaceLabelKey: ns,
				lib.GatewayTypeLabelKey:      "direct",
			},
		}},
	}.Gateway()
	gwUpdate.ResourceVersion = "2"
	if _, err := lib.GetAdvL4Clientset().NetworkingV1alpha1pre1().Gateways(ns).Update(gwUpdate); err != nil {
		t.Fatalf("error in updating Gateway: %v", err)
	}

	g.Eventually(func() bool {
		found, aviModel := objects.SharedAviGraphLister().Get(modelName)
		if found && aviModel == nil {
			return false
		}
		return true
	}, 20*time.Second).Should(gomega.Equal(false))

	// match service to chaged gateway protocol: UDP
	svcUpdate := integrationtest.FakeService{
		Name:      "svc",
		Namespace: ns,
		Labels: map[string]string{
			lib.GatewayNameLabelKey:      gatewayName,
			lib.GatewayNamespaceLabelKey: ns,
			lib.GatewayTypeLabelKey:      "direct",
		},
		Type:         corev1.ServiceTypeLoadBalancer,
		ServicePorts: []integrationtest.Serviceport{{PortName: "foo1", Protocol: corev1.ProtocolUDP, PortNumber: 8081, TargetPort: 8081}},
	}.Service()
	fmt.Println(utils.Stringify(svcUpdate))
	svcUpdate.ResourceVersion = "2"
	if _, err := KubeClient.CoreV1().Services(ns).Update(svcUpdate); err != nil {
		t.Fatalf("error in updating Service: %v", err)
	}

	g.Eventually(func() bool {
		if found, aviModel := objects.SharedAviGraphLister().Get(modelName); found && aviModel != nil {
			nodes := aviModel.(*avinodes.AviObjectGraph).GetAviVS()
			if len(nodes) > 0 &&
				len(nodes[0].PoolRefs) == 1 &&
				nodes[0].PoolRefs[0].Protocol == "UDP" {
				return true
			}
		}
		return false
	}, 10*time.Second).Should(gomega.Equal(true))

	TeardownAdvLBService(t, "svc", ns)
	TeardownGateway(t, gatewayName, ns)
	TeardownGatewayClass(t, gwClassName)
	VerifyGatewayVSNodeDeletion(g, modelName)
}
