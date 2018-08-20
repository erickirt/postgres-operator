package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"

	log "github.com/Sirupsen/logrus"
	crv1 "github.com/crunchydata/postgres-operator/apis/cr/v1"
	"github.com/crunchydata/postgres-operator/apiserver"
	msgs "github.com/crunchydata/postgres-operator/apiservermsgs"
	clientset "github.com/crunchydata/postgres-operator/client"

	"github.com/crunchydata/postgres-operator/kubeapi"

	_ "github.com/lib/pq"
	"k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"k8s.io/client-go/tools/clientcmd"
)

const (
	replInfoQueryFormat = "SELECT %s(%s(), '0/0')::bigint, %s(%s(), '0/0')::bigint"

	recvV9         = "pg_last_xlog_receive_location"
	replayV9       = "pg_last_xlog_replay_location"
	locationDiffV9 = "pg_xlog_location_diff"

	recvV10         = "pg_last_wal_receive_lsn"
	replayV10       = "pg_last_wal_replay_lsn"
	locationDiffV10 = "pg_wal_lsn_diff"
)

type ReplicationInfo struct {
	ReceiveLocation uint64
	ReplayLocation  uint64
}

var (
	kubeconfig = flag.String("kubeconfig", "./config", "absolute path to the kubeconfig file")
)

func main() {
	flag.Parse()
	fmt.Println("hi")

	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	kubeClient, err2 := kubernetes.NewForConfig(config)
	if err2 != nil {
		panic(err2.Error())
	}
	if kubeClient != nil {
		log.Println("got kube client")
	}
	restclient, _, err := clientset.NewClient(config)
	if err != nil {
		panic(err)
	}
	log.Println("got rest client")

	namespace := "demo"
	//get the secrets for this cluster
	clusterName := "nake"

	selector := "primary=true,pg-cluster=" + clusterName
	//get the pgcluster
	cluster := crv1.Pgcluster{}
	var clusterfound bool
	clusterfound, err = kubeapi.Getpgcluster(restclient, &cluster, clusterName, namespace)
	if err != nil || !clusterfound {
		fmt.Println("Getpgcluster error: " + err.Error())
		os.Exit(2)
	} else {
		fmt.Println("pgcluster found " + clusterName)
	}
	//get the secrets for that pgcluster
	var secretInfo []msgs.ShowUserSecret
	apiserver.Clientset = kubeClient
	secretInfo, err = apiserver.GetSecrets(&cluster)
	var pgSecret msgs.ShowUserSecret
	var found bool
	for _, si := range secretInfo {
		if si.Username == "postgres" {
			pgSecret = si
			found = true
			fmt.Println("postgres secret found")
		}
	}

	if !found {
		fmt.Println("postgres secret not found for " + clusterName)
		os.Exit(2)
	} else {
		fmt.Println("found postgres secret with password " + pgSecret.Password)
	}

	selector = "primary=false,pg-cluster=" + clusterName
	podList, err := kubeapi.GetPods(kubeClient, selector, namespace)
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(2)
	}

	var selectedReplica v1.Pod
	if len(podList.Items) > 0 {
		selectedReplica = podList.Items[0]
	} else {
		fmt.Println("no replicas found")
		os.Exit(2)
	}

	var value uint64 = 0
	databaseName := "postgres"
	port := "5432"
	for _, pod := range podList.Items {
		fmt.Println(pod.Name)

		target := getSQLTarget(&pod, pgSecret.Username, pgSecret.Password, port, databaseName)
		replInfo, err := GetReplicationInfo(target)
		if err != nil {
			fmt.Println(err.Error())
		} else {
			fmt.Printf("receive location=%d replaylocation=%d\n", replInfo.ReceiveLocation, replInfo.ReplayLocation)
			if replInfo.ReceiveLocation > value {
				value = replInfo.ReceiveLocation
				selectedReplica = pod
			}
		}
	}
	fmt.Println("selected replica pod name is " + selectedReplica.Name)
}

func GetReplicationInfo(target string) (*ReplicationInfo, error) {
	conn, err := sql.Open("postgres", target)

	if err != nil {
		log.Errorf("Could not connect to: %s", target)
		return nil, err
	}

	defer conn.Close()

	// Get PG version
	var version int

	rows, err := conn.Query("SELECT current_setting('server_version_num')")

	if err != nil {
		log.Errorf("Could not perform query for version: %s", target)
		return nil, err
	}

	defer rows.Close()

	for rows.Next() {
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
	}
	// Get replication info
	var replicationInfoQuery string
	var recvLocation uint64
	var replayLocation uint64

	if version < 100000 {
		replicationInfoQuery = fmt.Sprintf(
			replInfoQueryFormat,
			locationDiffV9, recvV9,
			locationDiffV9, replayV9,
		)
	} else {
		replicationInfoQuery = fmt.Sprintf(
			replInfoQueryFormat,
			locationDiffV10, recvV10,
			locationDiffV10, replayV10,
		)
	}

	rows, err = conn.Query(replicationInfoQuery)

	if err != nil {
		log.Errorf("Could not perform replication info query: %s", target)
		return nil, err
	}

	defer rows.Close()

	for rows.Next() {
		if err := rows.Scan(&recvLocation, &replayLocation); err != nil {
			return nil, err
		}
	}

	return &ReplicationInfo{recvLocation, replayLocation}, nil
}

func getSQLTarget(pod *v1.Pod, username, password, port, db string) string {
	target := fmt.Sprintf(
		"postgresql://%s:%s@%s:%s/%s?sslmode=disable",
		username,
		password,
		pod.Status.PodIP,
		port,
		db,
	)
	return target

}
