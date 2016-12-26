// Copyright 2016 The etcd-operator Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cluster

import (
	"fmt"

	"github.com/Sirupsen/logrus"
	"github.com/coreos/etcd-operator/pkg/cluster/backupstorage"
	"github.com/coreos/etcd-operator/pkg/spec"
	"github.com/coreos/etcd-operator/pkg/util/k8sutil"
)

type backupManager struct {
	logger *logrus.Entry

	clusterConfig Config
	backupPolicy  *spec.BackupPolicy
	restorePolicy *spec.RestorePolicy
	s             backupstorage.Storage
}

func newBackupManager(c Config, b *spec.BackupPolicy, r *spec.RestorePolicy, l *logrus.Entry, isNewCluster bool) (*backupManager, error) {
	bm := &backupManager{
		clusterConfig: c,
		backupPolicy:  b,
		restorePolicy: r,
		logger:        l,
	}
	hasExist := false
	if !isNewCluster {
		hasExist = true
	} else if r != nil && r.BackupClusterName == c.Name {
		hasExist = true // we will reuse the storage to restore cluster
	}
	var err error
	bm.s, err = bm.setupStorage(hasExist)
	if err != nil {
		return nil, err
	}
	return bm, nil
}

func (bm *backupManager) setup() error {
	if bm.s == nil && bm.restorePolicy != nil {
		bm.logger.Infof("storage is empty, cannot restore from existing backup (%s)", bm.restorePolicy.BackupClusterName)
		bm.restorePolicy = nil
	}

	if r := bm.restorePolicy; r != nil {
		bm.logger.Infof("restoring cluster from existing backup (%s)", r.BackupClusterName)
		if bm.clusterConfig.Name != r.BackupClusterName {
			if err := bm.s.Clone(r.BackupClusterName); err != nil {
				return err
			}
		}
	}

	return bm.runSidecar()
}

func (bm *backupManager) setupStorage(hasExist bool) (s backupstorage.Storage, err error) {
	b, c := bm.backupPolicy, bm.clusterConfig

	switch b.StorageType {
	case spec.BackupStorageTypePersistentVolume:
		s, err = backupstorage.NewPVStorage(c.KubeCli, c.Name, c.Namespace, c.PVProvisioner, *b, hasExist)
	case spec.BackupStorageTypeS3:
		s, err = backupstorage.NewS3Storage(c.S3Context, c.KubeCli, c.Name, c.Namespace, *b, hasExist)
	case spec.BackupStorageTypeDefault:
		return nil, nil
	}
	return s, err
}

func (bm *backupManager) runSidecar() error {
	c := bm.clusterConfig
	podSpec, err := k8sutil.MakeBackupPodSpec(c.Name, bm.backupPolicy)
	if err != nil {
		return err
	}
	switch bm.backupPolicy.StorageType {
	case spec.BackupStorageTypePersistentVolume:
		podSpec = k8sutil.PodSpecWithPV(podSpec, c.Name)
	case spec.BackupStorageTypeS3:
		podSpec = k8sutil.PodSpecWithS3(podSpec, c.S3Context)
	case spec.BackupStorageTypeDefault:
		podSpec = k8sutil.PodSpecWithEmptyDir(podSpec)
	}
	err = k8sutil.CreateBackupReplicaSetAndService(c.KubeCli, c.Name, c.Namespace, *podSpec)
	if err != nil {
		return fmt.Errorf("failed to create backup replica set and service: %v", err)
	}
	bm.logger.Info("backup replica set and service created")
	return nil
}

func (bm *backupManager) cleanup() error {
	c := bm.clusterConfig
	err := k8sutil.DeleteBackupReplicaSetAndService(c.KubeCli, c.Name, c.Namespace)
	if err != nil {
		return fmt.Errorf("fail to delete backup ReplicaSet and Service: %v", err)
	}
	bm.logger.Infof("backup replica set and service deleted")

	if bm.s != nil {
		err = bm.s.Delete()
	}
	if err != nil {
		return fmt.Errorf("fail to delete store: %v", err)
	}
	return nil
}
