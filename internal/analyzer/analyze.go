package analyzer

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/kvesta/vesta/config"
	_image "github.com/kvesta/vesta/pkg/inspector"
	"github.com/kvesta/vesta/pkg/vulnlib"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (s *Scanner) Analyze(ctx context.Context, inspectors []*types.ContainerJSON, images []*_image.ImageInfo) error {

	err := s.checkDockerContext(ctx, images)
	if err != nil {
		log.Printf("failed to check docker context, error: %v", err)
	}

	log.Printf(config.Yellow("Begin container analyzing"))
	for _, in := range inspectors {
		err := s.checkDockerList(in)
		if err != nil {
			log.Printf("Container %s check error, %v", in.ID[:12], err)
		}
	}
	return nil
}

func (ks *KScanner) Kanalyze(ctx context.Context) error {

	err := ks.checkKubernetesList(ctx)
	if err != nil {
		return err
	}

	return nil
}

func (s *Scanner) checkDockerList(config *types.ContainerJSON) error {

	var isVulnerable = false
	ths := []*threat{}

	// Checking privileged
	if ok, tlist := checkPrivileged(config); ok {
		ths = append(ths, tlist...)
		isVulnerable = true
	}

	// Checking mount volumes
	if ok, tlist := checkMount(config); ok {
		ths = append(ths, tlist...)
		isVulnerable = true
	}

	// Check the strength of password
	if ok, tlist := checkEnvPassword(config); ok {
		ths = append(ths, tlist...)
		isVulnerable = true
	}

	// Checking network model
	if ok, tlist := checkNetworkModel(config, s.EngineVersion); ok {
		ths = append(ths, tlist...)
		isVulnerable = true
	}

	if ok, tlist := checkPid(config); ok {
		ths = append(ths, tlist...)
		isVulnerable = true
	}

	if isVulnerable {
		sortSeverity(ths)

		con := &container{
			ContainerID:   config.ID[:12],
			ContainerName: config.Name[1:],

			Threats: ths,
		}
		s.VulnContainers = append(s.VulnContainers, con)
	}

	return nil
}

func (ks *KScanner) checkKubernetesList(ctx context.Context) error {

	version, err := ks.KClient.ServerVersion()

	if err != nil {
		if strings.Contains(err.Error(), "connection refused") {
			log.Printf("kubelet is not start")
		} else {
			log.Printf("failed to start Kubernetes, error: %v", err)
		}
		return err
	}
	ks.Version = version.String()

	// If k8s version less than v1.24, using the docker checking
	if compareVersion(ks.Version, "1.24", "0.0") {
		err = ks.dockershimCheck(ctx)
		if err != nil {
			log.Printf("failed to use docker to check, error: %v", err)
		}
	} else {
		err = ks.kernelCheck(ctx)
		if err != nil {
			log.Printf("failed to check kernel version, error: %v", err)
		}
	}

	nsList, err := ks.KClient.
		CoreV1().
		Namespaces().List(context.TODO(), metav1.ListOptions{})

	if err != nil {
		log.Printf("get namespace failed: %v", err)
	}

	err = ks.getNodeInfor(ctx)
	if err != nil {
		log.Printf("failed to get node information: %v", err)
	}

	// Check RBAC rules
	err = ks.checkClusterBinding()
	if err != nil {
		log.Printf("check RBAC failed, %v", err)
	}

	log.Printf(config.Yellow("Begin Pods analyzing"))
	log.Printf(config.Yellow("Begin ConfigMap and Secret analyzing"))
	log.Printf(config.Yellow("Begin RoleBinding analyzing"))
	log.Printf(config.Yellow("Begin Job and CronJob analyzing"))
	log.Printf(config.Yellow("Begin DaemonSet analyzing"))

	if ctx.Value("nameSpace") == "all" {
		namespaceWhileList = []string{}
	}

	// Check configuration in namespace
	if ctx.Value("nameSpace") != "standard" && ctx.Value("nameSpace") != "all" {
		ns := ctx.Value("nameSpace")

		err = ks.checkRoleBinding(ns.(string))
		if err != nil {
			log.Printf("check role binding failed in namespace: %s, %v", ns.(string), err)
		}

		err = ks.checkConfigMap(ns.(string))
		if err != nil {
			log.Printf("check config map failed in namespace: %s, %v", ns.(string), err)
		}

		err = ks.checkSecret(ns.(string))
		if err != nil {
			log.Printf("check secret failed in namespace: %s, %v", ns.(string), err)
		}

		err := ks.checkPod(ns.(string))
		if err != nil {
			log.Printf("check pod failed in namespace: %s, %v", ns.(string), err)
		}

		err = ks.checkDaemonSet(ns.(string))
		if err != nil {
			log.Printf("check daemonset failed in namespace: %s, %v", ns.(string), err)
		}

		err = ks.checkJobsOrCornJob(ns.(string))
		if err != nil {
			log.Printf("check job failed in namespace: %s, %v", ns.(string), err)
		}

	} else {
		for _, ns := range nsList.Items {

			isNecessary := true

			// Check whether in the white list of namespaces
			for _, nswList := range namespaceWhileList {
				if ns.Name == nswList {
					isNecessary = false
				}
			}

			if isNecessary {
				err = ks.checkRoleBinding(ns.Name)
				if err != nil {
					log.Printf("check role binding failed in namespace: %s, %v", ns.Name, err)
				}

				// TODO: remove from the white list, add kube-system namespace checking
				err = ks.checkConfigMap(ns.Name)
				if err != nil {
					log.Printf("check config map failed in namespace: %s, %v", ns.Name, err)
				}

				// TODO: remove from the white list, add kube-system namespace checking
				err = ks.checkSecret(ns.Name)
				if err != nil {
					log.Printf("check secret failed in namespace %s, %v", ns.Name, err)
				}

				err := ks.checkPod(ns.Name)
				if err != nil {
					log.Printf("check pod failed in namespace: %s, %v", ns.Name, err)
				}

				err = ks.checkJobsOrCornJob(ns.Name)
				if err != nil {
					log.Printf("check job failed in namespace: %s, %v", ns.Name, err)
				}
			}

			err = ks.checkDaemonSet(ns.Name)
			if err != nil {
				log.Printf("check daemonset failed in namespace: %s, %v", ns.Name, err)
			}
		}
	}

	// Check PV and PVC
	err = ks.checkPersistentVolume()
	if err != nil {
		log.Printf("check pv and pvc failed, %v", err)
	}

	// Check certification expiration
	err = ks.checkCerts()
	if err != nil {
		log.Printf("check certification expiration failed, %v", err)
	}

	// Check Kubernetes CNI
	err = ks.checkCNI()
	if err != nil {
		log.Printf("check CNI failed, %v", err)
	}

	sortSeverity(ks.VulnConfigures)

	return nil
}

// checkDockerVersion check docker server version
func checkDockerVersion(cli vulnlib.Client, serverVersion string) (bool, []*threat) {
	log.Printf(config.Yellow("Begin docker version analyzing"))

	var vuln = false

	tlist := []*threat{}

	rows, err := cli.QueryVulnByName("docker")
	if err != nil {
		return vuln, tlist
	}

	for _, row := range rows {
		if compareVersion(serverVersion, row.MaxVersion, row.MinVersion) {
			th := &threat{
				Param:     "Docker server",
				Value:     serverVersion,
				Type:      "K8s version less than v1.24",
				Describe:  fmt.Sprintf("Docker server version is threated under the %s", row.CVEID),
				Reference: row.Description,
				Severity:  strings.ToLower(row.Level),
			}

			tlist = append(tlist, th)
			vuln = true
		}
	}

	return vuln, tlist
}

// checkKernelVersion check kernel version for whether the kernel version
// is under the vulnerable version which has a potential container escape
// such as Dirty Cow,Dirty Pipe
func checkKernelVersion(cli vulnlib.Client, kernelVersion string) (bool, []*threat) {
	var vuln = false

	tlist := []*threat{}

	var vulnKernelVersion = map[string]string{
		"CVE-2016-5195":  "Dirty Cow",
		"CVE-2020-14386": "CVE-2020-14386 with CAP_NET_RAW",
		"CVE-2021-22555": "CVE-2021-22555 kernel-netfilter",
		"CVE-2022-0847":  "Dirty Pipe",
		"CVE-2022-0185":  "CVE-2022-0185 with CAP_SYS_ADMIN",
		"CVE-2022-0492":  "CVE-2022-0492 with CAP_SYS_ADMIN and v1 architecture of cgroups"}

	log.Printf(config.Yellow("Begin kernel version analyzing"))
	for cve, nickname := range vulnKernelVersion {
		underVuln := false

		rows, err := cli.QueryVulnByCVEID(cve)
		if err != nil {
			log.Printf("faield to search database, error: %v", err)
			break
		}

		for _, row := range rows {

			// The data of CVE-2016-5195 is not correct
			if cve == "CVE-2016-5195" {
				row.MaxVersion = "4.8.3"
			}

			if compareVersion(kernelVersion, row.MaxVersion, row.MinVersion) {
				vuln, underVuln = true, true
			}
		}

		if underVuln {
			th := &threat{
				Param: "kernel version",
				Value: kernelVersion,
				Type:  "K8s version less than v1.24",
				Describe: fmt.Sprintf("Kernel version is suffering the %s vulnerablility, "+
					"has a potential container escape.", nickname),
				Reference: "Upload kernel version or docker-desktop.",
				Severity:  "critical",
			}

			tlist = append(tlist, th)
		}
	}

	return vuln, tlist
}
