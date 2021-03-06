// +build linux,cgo,!agent

package db

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lxc/lxd/lxd/db/query"
	deviceConfig "github.com/lxc/lxd/lxd/device/config"
	"github.com/lxc/lxd/lxd/instance/instancetype"
	"github.com/lxc/lxd/shared"
	"github.com/lxc/lxd/shared/api"
	"github.com/lxc/lxd/shared/logger"
	"github.com/pkg/errors"

	log "github.com/lxc/lxd/shared/log15"
)

// Code generation directives.
//
//go:generate -command mapper lxd-generate db mapper -t instances.mapper.go
//go:generate mapper reset
//
//go:generate mapper stmt -p db -e instance objects
//go:generate mapper stmt -p db -e instance objects-by-Project
//go:generate mapper stmt -p db -e instance objects-by-Project-and-Type
//go:generate mapper stmt -p db -e instance objects-by-Project-and-Type-and-Node
//go:generate mapper stmt -p db -e instance objects-by-Project-and-Type-and-Node-and-Name
//go:generate mapper stmt -p db -e instance objects-by-Project-and-Type-and-Name
//go:generate mapper stmt -p db -e instance objects-by-Project-and-Name
//go:generate mapper stmt -p db -e instance objects-by-Project-and-Name-and-Node
//go:generate mapper stmt -p db -e instance objects-by-Project-and-Node
//go:generate mapper stmt -p db -e instance objects-by-Type
//go:generate mapper stmt -p db -e instance objects-by-Type-and-Name
//go:generate mapper stmt -p db -e instance objects-by-Type-and-Name-and-Node
//go:generate mapper stmt -p db -e instance objects-by-Type-and-Node
//go:generate mapper stmt -p db -e instance objects-by-Node
//go:generate mapper stmt -p db -e instance objects-by-Node-and-Name
//go:generate mapper stmt -p db -e instance objects-by-Name
//go:generate mapper stmt -p db -e instance profiles-ref
//go:generate mapper stmt -p db -e instance profiles-ref-by-Project
//go:generate mapper stmt -p db -e instance profiles-ref-by-Node
//go:generate mapper stmt -p db -e instance profiles-ref-by-Project-and-Node
//go:generate mapper stmt -p db -e instance profiles-ref-by-Project-and-Name
//go:generate mapper stmt -p db -e instance config-ref
//go:generate mapper stmt -p db -e instance config-ref-by-Project
//go:generate mapper stmt -p db -e instance config-ref-by-Node
//go:generate mapper stmt -p db -e instance config-ref-by-Project-and-Node
//go:generate mapper stmt -p db -e instance config-ref-by-Project-and-Name
//go:generate mapper stmt -p db -e instance devices-ref
//go:generate mapper stmt -p db -e instance devices-ref-by-Project
//go:generate mapper stmt -p db -e instance devices-ref-by-Node
//go:generate mapper stmt -p db -e instance devices-ref-by-Project-and-Node
//go:generate mapper stmt -p db -e instance devices-ref-by-Project-and-Name
//go:generate mapper stmt -p db -e instance id
//go:generate mapper stmt -p db -e instance create struct=Instance
//go:generate mapper stmt -p db -e instance create-config-ref
//go:generate mapper stmt -p db -e instance create-devices-ref
//go:generate mapper stmt -p db -e instance rename
//go:generate mapper stmt -p db -e instance delete
//
//go:generate mapper method -p db -e instance List
//go:generate mapper method -p db -e instance Get
//go:generate mapper method -p db -e instance ID struct=Instance
//go:generate mapper method -p db -e instance Exists struct=Instance
//go:generate mapper method -p db -e instance Create struct=Instance
//go:generate mapper method -p db -e instance ProfilesRef
//go:generate mapper method -p db -e instance ConfigRef
//go:generate mapper method -p db -e instance DevicesRef
//go:generate mapper method -p db -e instance Rename
//go:generate mapper method -p db -e instance Delete

// Instance is a value object holding db-related details about a container.
type Instance struct {
	ID           int
	Project      string `db:"primary=yes&join=projects.name"`
	Name         string `db:"primary=yes"`
	Node         string `db:"join=nodes.name"`
	Type         instancetype.Type
	Snapshot     bool `db:"ignore"`
	Architecture int
	Ephemeral    bool
	CreationDate time.Time
	Stateful     bool
	LastUseDate  time.Time
	Description  string `db:"coalesce=''"`
	Config       map[string]string
	Devices      map[string]map[string]string
	Profiles     []string
	ExpiryDate   time.Time
}

// InstanceFilter can be used to filter results yielded by InstanceList.
type InstanceFilter struct {
	Project string
	Name    string
	Node    string
	Type    instancetype.Type
}

// InstanceToArgs is a convenience to convert an Instance db struct into the legacy InstanceArgs.
func InstanceToArgs(inst *Instance) InstanceArgs {
	args := InstanceArgs{
		ID:           inst.ID,
		Project:      inst.Project,
		Name:         inst.Name,
		Node:         inst.Node,
		Type:         inst.Type,
		Snapshot:     inst.Snapshot,
		Architecture: inst.Architecture,
		Ephemeral:    inst.Ephemeral,
		CreationDate: inst.CreationDate,
		Stateful:     inst.Stateful,
		LastUsedDate: inst.LastUseDate,
		Description:  inst.Description,
		Config:       inst.Config,
		Devices:      deviceConfig.NewDevices(inst.Devices),
		Profiles:     inst.Profiles,
		ExpiryDate:   inst.ExpiryDate,
	}

	if args.Devices == nil {
		args.Devices = deviceConfig.Devices{}
	}

	return args
}

// InstanceArgs is a value object holding all db-related details about an instance.
type InstanceArgs struct {
	// Don't set manually
	ID       int
	Node     string
	Type     instancetype.Type
	Snapshot bool

	// Creation only
	Project      string
	BaseImage    string
	CreationDate time.Time

	Architecture int
	Config       map[string]string
	Description  string
	Devices      deviceConfig.Devices
	Ephemeral    bool
	LastUsedDate time.Time
	Name         string
	Profiles     []string
	Stateful     bool
	ExpiryDate   time.Time
}

// InstanceBackupArgs is a value object holding all db-related details about a backup.
type InstanceBackupArgs struct {
	// Don't set manually
	ID int

	InstanceID           int
	Name                 string
	CreationDate         time.Time
	ExpiryDate           time.Time
	InstanceOnly         bool
	OptimizedStorage     bool
	CompressionAlgorithm string
}

// GetInstanceNames returns the names of all containers the given project.
func (c *ClusterTx) GetInstanceNames(project string) ([]string, error) {
	stmt := `
SELECT instances.name FROM instances
  JOIN projects ON projects.id = instances.project_id
  WHERE projects.name = ? AND instances.type = ?
`
	return query.SelectStrings(c.tx, stmt, project, instancetype.Any)
}

// GetNodeAddressOfInstance returns the address of the node hosting the
// instance with the given name in the given project.
//
// It returns the empty string if the container is hosted on this node.
func (c *ClusterTx) GetNodeAddressOfInstance(project string, name string, instanceType instancetype.Type) (string, error) {
	var stmt string

	args := make([]interface{}, 0, 4) // Expect up to 4 filters.
	var filters strings.Builder

	// Project filter.
	filters.WriteString("projects.name = ?")
	args = append(args, project)

	// Instance type filter.
	if instanceType != instancetype.Any {
		filters.WriteString(" AND instances.type = ?")
		args = append(args, instanceType)
	}

	if strings.Contains(name, shared.SnapshotDelimiter) {
		parts := strings.SplitN(name, shared.SnapshotDelimiter, 2)

		// Instance name filter.
		filters.WriteString(" AND instances.name = ?")
		args = append(args, parts[0])

		// Snapshot name filter.
		filters.WriteString(" AND instances_snapshots.name = ?")
		args = append(args, parts[1])

		stmt = fmt.Sprintf(`
SELECT nodes.id, nodes.address
  FROM nodes
  JOIN instances ON instances.node_id = nodes.id
  JOIN projects ON projects.id = instances.project_id
  JOIN instances_snapshots ON instances_snapshots.instance_id = instances.id
 WHERE %s
`, filters.String())
	} else {
		// Instance name filter.
		filters.WriteString(" AND instances.name = ?")
		args = append(args, name)

		stmt = fmt.Sprintf(`
SELECT nodes.id, nodes.address
  FROM nodes
  JOIN instances ON instances.node_id = nodes.id
  JOIN projects ON projects.id = instances.project_id
 WHERE %s
`, filters.String())
	}

	var address string
	var id int64
	rows, err := c.tx.Query(stmt, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	if !rows.Next() {
		return "", ErrNoSuchObject
	}

	err = rows.Scan(&id, &address)
	if err != nil {
		return "", err
	}

	if rows.Next() {
		return "", fmt.Errorf("More than one node associated with instance")
	}

	err = rows.Err()
	if err != nil {
		return "", err
	}

	if id == c.nodeID {
		return "", nil
	}

	return address, nil
}

// GetInstanceNamesByNodeAddress returns the names of all containers grouped by
// cluster node address.
//
// The node address of containers running on the local node is set to the empty
// string, to distinguish it from remote nodes.
//
// Containers whose node is down are addeded to the special address "0.0.0.0".
func (c *ClusterTx) GetInstanceNamesByNodeAddress(project string, instanceType instancetype.Type) (map[string][]string, error) {
	offlineThreshold, err := c.GetNodeOfflineThreshold()
	if err != nil {
		return nil, err
	}

	args := make([]interface{}, 0, 2) // Expect up to 2 filters.
	var filters strings.Builder

	// Project filter.
	filters.WriteString("projects.name = ?")
	args = append(args, project)

	// Instance type filter.
	if instanceType != instancetype.Any {
		filters.WriteString(" AND instances.type = ?")
		args = append(args, instanceType)
	}

	stmt := fmt.Sprintf(`
SELECT instances.name, nodes.id, nodes.address, nodes.heartbeat
  FROM instances
  JOIN nodes ON nodes.id = instances.node_id
  JOIN projects ON projects.id = instances.project_id
  WHERE %s
  ORDER BY instances.id
`, filters.String())

	rows, err := c.tx.Query(stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := map[string][]string{}

	for i := 0; rows.Next(); i++ {
		var name string
		var nodeAddress string
		var nodeID int64
		var nodeHeartbeat time.Time
		err := rows.Scan(&name, &nodeID, &nodeAddress, &nodeHeartbeat)
		if err != nil {
			return nil, err
		}
		if nodeID == c.nodeID {
			nodeAddress = ""
		} else if nodeIsOffline(offlineThreshold, nodeHeartbeat) {
			nodeAddress = "0.0.0.0"
		}
		result[nodeAddress] = append(result[nodeAddress], name)
	}

	err = rows.Err()
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Load all instances across all projects and expands their config and devices
// using the profiles they are associated to.
func (c *ClusterTx) instanceListExpanded() ([]Instance, error) {
	instances, err := c.GetInstances(InstanceFilter{})
	if err != nil {
		return nil, errors.Wrap(err, "Load instances")
	}

	projects, err := c.GetProjects(ProjectFilter{})
	if err != nil {
		return nil, errors.Wrap(err, "Load projects")
	}

	// Map to check which projects have the profiles features on.
	projectHasProfiles := map[string]bool{}
	for _, project := range projects {
		projectHasProfiles[project.Name] = shared.IsTrue(project.Config["features.profiles"])
	}

	profiles, err := c.GetProfiles(ProfileFilter{})
	if err != nil {
		return nil, errors.Wrap(err, "Load profiles")
	}

	// Index of all profiles by project and name.
	profilesByProjectAndName := map[string]map[string]Profile{}
	for _, profile := range profiles {
		profilesByName, ok := profilesByProjectAndName[profile.Project]
		if !ok {
			profilesByName = map[string]Profile{}
			profilesByProjectAndName[profile.Project] = profilesByName
		}
		profilesByName[profile.Name] = profile
	}

	for i, instance := range instances {
		profiles := make([]api.Profile, len(instance.Profiles))

		profilesProject := instance.Project

		// If the instance's project does not have the profiles feature
		// enable, we fall back to the default project.
		if !projectHasProfiles[profilesProject] {
			profilesProject = "default"
		}

		for j, name := range instance.Profiles {
			profile := profilesByProjectAndName[profilesProject][name]
			profiles[j] = *ProfileToAPI(&profile)
		}

		instances[i].Config = ExpandInstanceConfig(instance.Config, profiles)
		instances[i].Devices = ExpandInstanceDevices(deviceConfig.NewDevices(instance.Devices), profiles).CloneNative()
	}

	return instances, nil
}

// GetInstanceToNodeMap returns a map associating the name of each
// instance in the given project to the name of the node hosting the instance.
func (c *ClusterTx) GetInstanceToNodeMap(project string, instanceType instancetype.Type) (map[string]string, error) {
	args := make([]interface{}, 0, 2) // Expect up to 2 filters.
	var filters strings.Builder

	// Project filter.
	filters.WriteString("projects.name = ?")
	args = append(args, project)

	// Instance type filter.
	if instanceType != instancetype.Any {
		filters.WriteString(" AND instances.type = ?")
		args = append(args, instanceType)
	}

	stmt := fmt.Sprintf(`
SELECT instances.name, nodes.name
  FROM instances
  JOIN nodes ON nodes.id = instances.node_id
  JOIN projects ON projects.id = instances.project_id
  WHERE %s
`, filters.String())

	rows, err := c.tx.Query(stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := map[string]string{}

	for i := 0; rows.Next(); i++ {
		var name string
		var nodeName string
		err := rows.Scan(&name, &nodeName)
		if err != nil {
			return nil, err
		}
		result[name] = nodeName
	}

	err = rows.Err()
	if err != nil {
		return nil, err
	}
	return result, nil
}

// UpdateInstanceNode changes the node hosting an instance.
//
// It's meant to be used when moving a non-running instance backed by ceph
// from one cluster node to another.
func (c *ClusterTx) UpdateInstanceNode(project, oldName, newName, newNode string) error {
	// First check that the container to be moved is backed by a ceph
	// volume.
	poolName, err := c.GetInstancePool(project, oldName)
	if err != nil {
		return errors.Wrap(err, "Failed to get instance's storage pool name")
	}

	poolID, err := c.GetStoragePoolID(poolName)
	if err != nil {
		return errors.Wrap(err, "Failed to get instance's storage pool ID")
	}

	poolDriver, err := c.GetStoragePoolDriver(poolID)
	if err != nil {
		return errors.Wrap(err, "Failed to get instance's storage pool driver")
	}

	if poolDriver != "ceph" {
		return fmt.Errorf("Instance's storage pool is not of type ceph")
	}

	// Update the name of the container and of its snapshots, and the node
	// ID they are associated with.
	containerID, err := c.GetInstanceID(project, oldName)
	if err != nil {
		return errors.Wrap(err, "Failed to get instance's ID")
	}

	node, err := c.GetNodeByName(newNode)
	if err != nil {
		return errors.Wrap(err, "Failed to get new node's info")
	}

	stmt := "UPDATE instances SET node_id=?, name=? WHERE id=?"
	result, err := c.tx.Exec(stmt, node.ID, newName, containerID)
	if err != nil {
		return errors.Wrap(err, "Failed to update instance's name and node ID")
	}

	n, err := result.RowsAffected()
	if err != nil {
		return errors.Wrap(err, "Failed to get rows affected by instance update")
	}

	if n != 1 {
		return fmt.Errorf("Unexpected number of updated rows in instances table: %d", n)
	}

	// No need to update storage_volumes if the name is identical
	if newName == oldName {
		return nil
	}

	// Update the instance's storage volume name (since this is ceph,
	// there's a clone of the volume for each node).
	count, err := c.GetNodesCount()
	if err != nil {
		return errors.Wrap(err, "Failed to get node's count")
	}

	stmt = "UPDATE storage_volumes SET name=? WHERE name=? AND storage_pool_id=? AND type=?"
	result, err = c.tx.Exec(stmt, newName, oldName, poolID, StoragePoolVolumeTypeContainer)
	if err != nil {
		return errors.Wrap(err, "Failed to update instance's volume name")
	}

	n, err = result.RowsAffected()
	if err != nil {
		return errors.Wrap(err, "Failed to get rows affected by instance volume update")
	}

	if n != int64(count) {
		return fmt.Errorf("Unexpected number of updated rows in volumes table: %d", n)
	}

	return nil
}

// GetLocalInstancesInProject retuurns all instances of the given type on the
// local node within the given project.
func (c *ClusterTx) GetLocalInstancesInProject(project string, instanceType instancetype.Type) ([]Instance, error) {
	node, err := c.GetLocalNodeName()
	if err != nil {
		return nil, errors.Wrap(err, "Local node name")
	}

	filter := InstanceFilter{
		Project: project,
		Node:    node,
		Type:    instanceType,
	}

	return c.GetInstances(filter)
}

// CreateInstanceConfig inserts a new config for the container with the given ID.
func (c *ClusterTx) CreateInstanceConfig(id int, config map[string]string) error {
	return CreateInstanceConfig(c.tx, id, config)
}

// UpdateInstanceConfig inserts/updates/deletes the provided keys
func (c *ClusterTx) UpdateInstanceConfig(id int, values map[string]string) error {
	insertSQL := fmt.Sprintf("INSERT OR REPLACE INTO instances_config (instance_id, key, value) VALUES")
	deleteSQL := "DELETE FROM instances_config WHERE key IN %s AND instance_id=?"
	return c.configUpdate(id, values, insertSQL, deleteSQL)
}

func (c *ClusterTx) configUpdate(id int, values map[string]string, insertSQL, deleteSQL string) error {
	changes := map[string]string{}
	deletes := []string{}

	// Figure out which key to set/unset
	for key, value := range values {
		if value == "" {
			deletes = append(deletes, key)
			continue
		}
		changes[key] = value
	}

	// Insert/update keys
	if len(changes) > 0 {
		query := insertSQL
		exprs := []string{}
		params := []interface{}{}
		for key, value := range changes {
			exprs = append(exprs, "(?, ?, ?)")
			params = append(params, []interface{}{id, key, value}...)
		}

		query += strings.Join(exprs, ",")
		_, err := c.tx.Exec(query, params...)
		if err != nil {
			return err
		}
	}

	// Delete keys
	if len(deletes) > 0 {
		query := fmt.Sprintf(deleteSQL, query.Params(len(deletes)))
		params := []interface{}{}
		for _, key := range deletes {
			params = append(params, key)
		}

		params = append(params, id)
		_, err := c.tx.Exec(query, params...)
		if err != nil {
			return err
		}
	}

	return nil
}

// DeleteInstance removes the instance with the given name from the database.
func (c *Cluster) DeleteInstance(project, name string) error {
	if strings.Contains(name, shared.SnapshotDelimiter) {
		parts := strings.SplitN(name, shared.SnapshotDelimiter, 2)
		return c.Transaction(func(tx *ClusterTx) error {
			return tx.DeleteInstanceSnapshot(project, parts[0], parts[1])
		})
	}
	return c.Transaction(func(tx *ClusterTx) error {
		return tx.DeleteInstance(project, name)
	})
}

// GetInstanceProjectAndName returns the project and the name of the instance
// with the given ID.
func (c *Cluster) GetInstanceProjectAndName(id int) (string, string, error) {
	q := `
SELECT projects.name, instances.name
  FROM instances
  JOIN projects ON projects.id = instances.project_id
WHERE instances.id=?
`
	project := ""
	name := ""
	arg1 := []interface{}{id}
	arg2 := []interface{}{&project, &name}
	err := dbQueryRowScan(c.db, q, arg1, arg2)
	if err == sql.ErrNoRows {
		return "", "", ErrNoSuchObject
	}

	return project, name, err
}

// GetInstanceID returns the ID of the instance with the given name.
func (c *Cluster) GetInstanceID(project, name string) (int, error) {
	var id int64
	err := c.Transaction(func(tx *ClusterTx) error {
		var err error
		id, err = tx.GetInstanceID(project, name)
		return err
	})
	return int(id), err
}

// DeleteInstanceConfig removes any config associated with the instance with
// the given ID.
func DeleteInstanceConfig(tx *sql.Tx, id int) error {
	_, err := tx.Exec("DELETE FROM instances_config WHERE instance_id=?", id)
	if err != nil {
		return err
	}
	_, err = tx.Exec("DELETE FROM instances_profiles WHERE instance_id=?", id)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM instances_devices_config WHERE id IN
		(SELECT instances_devices_config.id
		 FROM instances_devices_config JOIN instances_devices
		 ON instances_devices_config.instance_device_id=instances_devices.id
		 WHERE instances_devices.instance_id=?)`, id)
	if err != nil {
		return err
	}
	_, err = tx.Exec("DELETE FROM instances_devices WHERE instance_id=?", id)
	return err
}

// CreateInstanceConfig inserts a new config for the instance with the given ID.
func CreateInstanceConfig(tx *sql.Tx, id int, config map[string]string) error {
	stmt, err := tx.Prepare("INSERT INTO instances_config (instance_id, key, value) values (?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for k, v := range config {
		if v == "" {
			continue
		}

		_, err := stmt.Exec(id, k, v)
		if err != nil {
			logger.Debugf("Error adding configuration item %s = %s to container %d",
				k, v, id)
			return err
		}
	}

	return nil
}

// GetInstanceConfig returns the value of the given key in the configuration
// of the instance with the given ID.
func (c *Cluster) GetInstanceConfig(id int, key string) (string, error) {
	q := "SELECT value FROM instances_config WHERE instance_id=? AND key=?"
	value := ""
	arg1 := []interface{}{id, key}
	arg2 := []interface{}{&value}
	err := dbQueryRowScan(c.db, q, arg1, arg2)
	if err == sql.ErrNoRows {
		return "", ErrNoSuchObject
	}

	return value, err
}

// DeleteInstanceConfigKey removes the given key from the config of the instance
// with the given ID.
func (c *Cluster) DeleteInstanceConfigKey(id int, key string) error {
	err := exec(c.db, "DELETE FROM instances_config WHERE key=? AND instance_id=?", key, id)
	return err
}

// UpdateInstanceStatefulFlag toggles the stateful flag of the instance with
// the given ID.
func (c *Cluster) UpdateInstanceStatefulFlag(id int, stateful bool) error {
	statefulInt := 0
	if stateful {
		statefulInt = 1
	}

	err := exec(c.db, "UPDATE instances SET stateful=? WHERE id=?", statefulInt, id)
	return err
}

// AddProfilesToInstance associates the instance with the given ID with the
// profiles with the given names in the given project.
func AddProfilesToInstance(tx *sql.Tx, id int, project string, profiles []string) error {
	enabled, err := projectHasProfiles(tx, project)
	if err != nil {
		return errors.Wrap(err, "Check if project has profiles")
	}
	if !enabled {
		project = "default"
	}

	applyOrder := 1
	str := `
INSERT INTO instances_profiles (instance_id, profile_id, apply_order)
  VALUES (
    ?,
    (SELECT profiles.id
     FROM profiles
     JOIN projects ON projects.id=profiles.project_id
     WHERE projects.name=? AND profiles.name=?),
    ?
  )
`
	stmt, err := tx.Prepare(str)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, profile := range profiles {
		_, err = stmt.Exec(id, project, profile, applyOrder)
		if err != nil {
			logger.Debugf("Error adding profile %s to container: %s",
				profile, err)
			return err
		}
		applyOrder = applyOrder + 1
	}

	return nil
}

// LegacyContainersList returns the names of all the containers.
//
// NOTE: this is a pre-projects legacy API that is used only by patches. Don't
// use it for new code.
func (c *Cluster) LegacyContainersList() ([]string, error) {
	q := fmt.Sprintf("SELECT name FROM instances WHERE type=? ORDER BY name")
	inargs := []interface{}{instancetype.Container}
	var container string
	outfmt := []interface{}{container}
	result, err := queryScan(c.db, q, inargs, outfmt)
	if err != nil {
		return nil, err
	}

	var ret []string
	for _, container := range result {
		ret = append(ret, container[0].(string))
	}

	return ret, nil
}

// LegacySnapshotsList returns the names of all the snapshots.
//
// NOTE: this is a pre-projects legacy API that is used only by patches. Don't
// use it for new code.
func (c *Cluster) LegacySnapshotsList() ([]string, error) {
	q := fmt.Sprintf(`
SELECT instances.name, instances_snapshots.name
FROM instances_snapshots
JOIN instances ON instances.id = instances_snapshots.instance_id
WHERE type=? ORDER BY instances.name, instances_snapshots.name
`)
	inargs := []interface{}{instancetype.Container}
	var container string
	var snapshot string
	outfmt := []interface{}{container, snapshot}
	result, err := queryScan(c.db, q, inargs, outfmt)
	if err != nil {
		return nil, err
	}

	var ret []string
	for _, r := range result {
		ret = append(ret, r[0].(string)+shared.SnapshotDelimiter+r[1].(string))
	}

	return ret, nil
}

// ResetInstancesPowerState resets the power state of all instances.
func (c *Cluster) ResetInstancesPowerState() error {
	// Reset all container states
	err := exec(c.db, "DELETE FROM instances_config WHERE key='volatile.last_state.power'")
	return err
}

// UpdateInstancePowerState sets the the power state of the instance with the
// given ID.
func (c *Cluster) UpdateInstancePowerState(id int, state string) error {
	err := c.Transaction(func(tx *ClusterTx) error {
		return tx.UpdateInstancePowerState(id, state)
	})
	return err
}

// UpdateInstancePowerState sets the the power state of the container with the given ID.
func (c *ClusterTx) UpdateInstancePowerState(id int, state string) error {
	// Set the new value
	str := fmt.Sprintf("INSERT OR REPLACE INTO instances_config (instance_id, key, value) VALUES (?, 'volatile.last_state.power', ?)")
	stmt, err := c.tx.Prepare(str)
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(id, state)
	if err != nil {
		return err
	}

	return nil
}

// UpdateInstance updates the description, architecture and ephemeral flag of
// the instance with the given ID.
func UpdateInstance(tx *sql.Tx, id int, description string, architecture int, ephemeral bool,
	expiryDate time.Time) error {
	str := fmt.Sprintf("UPDATE instances SET description=?, architecture=?, ephemeral=?, expiry_date=? WHERE id=?")
	stmt, err := tx.Prepare(str)
	if err != nil {
		return err
	}
	defer stmt.Close()

	ephemeralInt := 0
	if ephemeral {
		ephemeralInt = 1
	}

	if expiryDate.IsZero() {
		_, err = stmt.Exec(description, architecture, ephemeralInt, "", id)
	} else {
		_, err = stmt.Exec(description, architecture, ephemeralInt, expiryDate, id)
	}
	if err != nil {
		return err
	}

	return nil
}

// UpdateInstanceSnapshotCreationDate updates the creation_date field of the instance snapshot with ID.
func (c *Cluster) UpdateInstanceSnapshotCreationDate(instanceID int, date time.Time) error {
	stmt := `UPDATE instances_snapshots SET creation_date=? WHERE id=?`
	err := exec(c.db, stmt, date, instanceID)
	return err
}

// UpdateInstanceLastUsedDate updates the last_use_date field of the instance
// with the given ID.
func (c *ClusterTx) UpdateInstanceLastUsedDate(id int, date time.Time) error {
	str := `UPDATE instances SET last_use_date=? WHERE id=?`
	stmt, err := c.tx.Prepare(str)
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(date, id)
	if err != nil {
		return err
	}

	return nil
}

// GetInstanceSnapshotsNames returns the names of all snapshots of the instance
// in the given project with the given name.
func (c *Cluster) GetInstanceSnapshotsNames(project, name string) ([]string, error) {
	result := []string{}

	q := `
SELECT instances_snapshots.name
  FROM instances_snapshots
  JOIN instances ON instances.id = instances_snapshots.instance_id
  JOIN projects ON projects.id = instances.project_id
WHERE projects.name=? AND instances.name=?
ORDER BY date(instances_snapshots.creation_date)
`
	inargs := []interface{}{project, name}
	outfmt := []interface{}{name}
	dbResults, err := queryScan(c.db, q, inargs, outfmt)
	if err != nil {
		return result, err
	}

	for _, r := range dbResults {
		result = append(result, name+shared.SnapshotDelimiter+r[0].(string))
	}

	return result, nil
}

// GetInstanceSnapshotsWithName returns all snapshots of a given instance.
func (c *ClusterTx) GetInstanceSnapshotsWithName(project string, name string) ([]Instance, error) {
	instance, err := c.GetInstance(project, name)
	if err != nil {
		return nil, err
	}
	filter := InstanceSnapshotFilter{
		Project:  project,
		Instance: name,
	}

	snapshots, err := c.GetInstanceSnapshots(filter)
	if err != nil {
		return nil, err
	}

	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].CreationDate.Before(snapshots[j].CreationDate) })

	instances := make([]Instance, len(snapshots))
	for i, snapshot := range snapshots {
		instances[i] = InstanceSnapshotToInstance(instance, &snapshot)
	}

	return instances, nil
}

// GetNextInstanceSnapshotIndex returns the index that the next snapshot of the
// instance with the given name and pattern should have.
func (c *Cluster) GetNextInstanceSnapshotIndex(project string, name string, pattern string) int {
	q := `
SELECT instances_snapshots.name
  FROM instances_snapshots
  JOIN instances ON instances.id = instances_snapshots.instance_id
  JOIN projects ON projects.id = instances.project_id
WHERE projects.name=? AND instances.name=?`
	var numstr string
	inargs := []interface{}{project, name}
	outfmt := []interface{}{numstr}
	results, err := queryScan(c.db, q, inargs, outfmt)
	if err != nil {
		return 0
	}
	max := 0

	for _, r := range results {
		snapOnlyName := r[0].(string)
		fields := strings.SplitN(pattern, "%d", 2)

		var num int
		count, err := fmt.Sscanf(snapOnlyName, fmt.Sprintf("%s%%d%s", fields[0], fields[1]), &num)
		if err != nil || count != 1 {
			continue
		}
		if num >= max {
			max = num + 1
		}
	}

	return max
}

// GetInstancePool returns the storage pool of a given instance.
//
// This is a non-transactional variant of ClusterTx.GetInstancePool().
func (c *Cluster) GetInstancePool(project, instanceName string) (string, error) {
	var poolName string
	err := c.Transaction(func(tx *ClusterTx) error {
		var err error
		poolName, err = tx.GetInstancePool(project, instanceName)
		return err
	})
	return poolName, err
}

// GetInstancePool returns the storage pool of a given instance.
func (c *ClusterTx) GetInstancePool(project, instanceName string) (string, error) {
	if strings.Contains(instanceName, shared.SnapshotDelimiter) {
		return c.instancePoolSnapshot(project, instanceName)
	}

	// Get container storage volume. Since container names are globally
	// unique, and their storage volumes carry the same name, their storage
	// volumes are unique too.
	poolName := ""
	query := `
SELECT storage_pools.name FROM storage_pools
  JOIN storage_volumes_all ON storage_pools.id=storage_volumes_all.storage_pool_id
  JOIN instances ON instances.name=storage_volumes_all.name
  JOIN projects ON projects.id=instances.project_id
 WHERE projects.name=?
   AND storage_volumes_all.node_id=?
   AND storage_volumes_all.name=?
   AND storage_volumes_all.type IN(?,?)
`
	inargs := []interface{}{project, c.nodeID, instanceName, StoragePoolVolumeTypeContainer, StoragePoolVolumeTypeVM}
	outargs := []interface{}{&poolName}

	err := c.tx.QueryRow(query, inargs...).Scan(outargs...)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", ErrNoSuchObject
		}

		return "", err
	}

	return poolName, nil
}

func (c *ClusterTx) instancePoolSnapshot(project, fullName string) (string, error) {
	poolName := ""
	query := `
SELECT storage_pools.name FROM storage_pools
  JOIN storage_volumes_all ON storage_pools.id=storage_volumes_all.storage_pool_id
  JOIN projects ON projects.id=storage_volumes_all.project_id
 WHERE projects.name=?
   AND storage_volumes_all.node_id=?
   AND storage_volumes_all.name=?
   AND storage_volumes_all.type IN(?,?)
`
	inargs := []interface{}{project, c.nodeID, fullName, StoragePoolVolumeTypeContainer, StoragePoolVolumeTypeVM}
	outargs := []interface{}{&poolName}

	err := c.tx.QueryRow(query, inargs...).Scan(outargs...)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", ErrNoSuchObject
		}

		return "", err
	}

	return poolName, nil
}

// Returns the ID of the instance backup with the given name.
func (c *Cluster) getInstanceBackupID(name string) (int, error) {
	q := "SELECT id FROM instances_backups WHERE name=?"
	id := -1
	arg1 := []interface{}{name}
	arg2 := []interface{}{&id}
	err := dbQueryRowScan(c.db, q, arg1, arg2)
	if err == sql.ErrNoRows {
		return -1, ErrNoSuchObject
	}

	return id, err
}

// GetInstanceBackup returns the backup with the given name.
func (c *Cluster) GetInstanceBackup(project, name string) (InstanceBackupArgs, error) {
	args := InstanceBackupArgs{}
	args.Name = name

	instanceOnlyInt := -1
	optimizedStorageInt := -1
	q := `
SELECT instances_backups.id, instances_backups.instance_id,
       instances_backups.creation_date, instances_backups.expiry_date,
       instances_backups.container_only, instances_backups.optimized_storage
    FROM instances_backups
    JOIN instances ON instances.id=instances_backups.instance_id
    JOIN projects ON projects.id=instances.project_id
    WHERE projects.name=? AND instances_backups.name=?
`
	arg1 := []interface{}{project, name}
	arg2 := []interface{}{&args.ID, &args.InstanceID, &args.CreationDate,
		&args.ExpiryDate, &instanceOnlyInt, &optimizedStorageInt}
	err := dbQueryRowScan(c.db, q, arg1, arg2)
	if err != nil {
		if err == sql.ErrNoRows {
			return args, ErrNoSuchObject
		}

		return args, err
	}

	if instanceOnlyInt == 1 {
		args.InstanceOnly = true
	}

	if optimizedStorageInt == 1 {
		args.OptimizedStorage = true
	}

	return args, nil
}

// GetInstanceBackups returns the names of all backups of the instance with the
// given name.
func (c *Cluster) GetInstanceBackups(project, name string) ([]string, error) {
	var result []string

	q := `SELECT instances_backups.name FROM instances_backups
JOIN instances ON instances_backups.instance_id=instances.id
JOIN projects ON projects.id=instances.project_id
WHERE projects.name=? AND instances.name=?`
	inargs := []interface{}{project, name}
	outfmt := []interface{}{name}
	dbResults, err := queryScan(c.db, q, inargs, outfmt)
	if err != nil {
		return nil, err
	}

	for _, r := range dbResults {
		result = append(result, r[0].(string))
	}

	return result, nil
}

// CreateInstanceBackup creates a new backup.
func (c *Cluster) CreateInstanceBackup(args InstanceBackupArgs) error {
	_, err := c.getInstanceBackupID(args.Name)
	if err == nil {
		return ErrAlreadyDefined
	}

	err = c.Transaction(func(tx *ClusterTx) error {
		instanceOnlyInt := 0
		if args.InstanceOnly {
			instanceOnlyInt = 1
		}

		optimizedStorageInt := 0
		if args.OptimizedStorage {
			optimizedStorageInt = 1
		}

		str := fmt.Sprintf("INSERT INTO instances_backups (instance_id, name, creation_date, expiry_date, container_only, optimized_storage) VALUES (?, ?, ?, ?, ?, ?)")
		stmt, err := tx.tx.Prepare(str)
		if err != nil {
			return err
		}
		defer stmt.Close()
		result, err := stmt.Exec(args.InstanceID, args.Name,
			args.CreationDate.Unix(), args.ExpiryDate.Unix(), instanceOnlyInt,
			optimizedStorageInt)
		if err != nil {
			return err
		}

		_, err = result.LastInsertId()
		if err != nil {
			return fmt.Errorf("Error inserting %q into database", args.Name)
		}

		return nil
	})

	return err
}

// DeleteInstanceBackup removes the instance backup with the given name from the database.
func (c *Cluster) DeleteInstanceBackup(name string) error {
	id, err := c.getInstanceBackupID(name)
	if err != nil {
		return err
	}

	err = exec(c.db, "DELETE FROM instances_backups WHERE id=?", id)
	if err != nil {
		return err
	}

	return nil
}

// RenameInstanceBackup renames an instance backup from the given current name
// to the new one.
func (c *Cluster) RenameInstanceBackup(oldName, newName string) error {
	err := c.Transaction(func(tx *ClusterTx) error {
		str := fmt.Sprintf("UPDATE instances_backups SET name = ? WHERE name = ?")
		stmt, err := tx.tx.Prepare(str)
		if err != nil {
			return err
		}
		defer stmt.Close()

		logger.Debug(
			"Calling SQL Query",
			log.Ctx{
				"query":   "UPDATE instances_backups SET name = ? WHERE name = ?",
				"oldName": oldName,
				"newName": newName})
		if _, err := stmt.Exec(newName, oldName); err != nil {
			return err
		}

		return nil
	})
	return err
}

// GetExpiredInstanceBackups returns a list of expired instance backups.
func (c *Cluster) GetExpiredInstanceBackups() ([]InstanceBackupArgs, error) {
	var result []InstanceBackupArgs
	var name string
	var expiryDate string
	var instanceID int

	q := `SELECT instances_backups.name, instances_backups.expiry_date, instances_backups.instance_id FROM instances_backups`
	outfmt := []interface{}{name, expiryDate, instanceID}
	dbResults, err := queryScan(c.db, q, nil, outfmt)
	if err != nil {
		return nil, err
	}

	for _, r := range dbResults {
		timestamp := r[1]

		var backupExpiry time.Time
		err = backupExpiry.UnmarshalText([]byte(timestamp.(string)))
		if err != nil {
			return []InstanceBackupArgs{}, err
		}

		// Since zero time causes some issues due to timezones, we check the
		// unix timestamp instead of IsZero().
		if backupExpiry.Unix() <= 0 {
			// Backup doesn't expire
			continue
		}

		// Backup has expired
		if time.Now().Unix()-backupExpiry.Unix() >= 0 {
			result = append(result, InstanceBackupArgs{
				Name:       r[0].(string),
				InstanceID: r[2].(int),
				ExpiryDate: backupExpiry,
			})
		}
	}

	return result, nil
}
