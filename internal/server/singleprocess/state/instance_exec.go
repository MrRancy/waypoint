package state

import (
	"io"
	"sync/atomic"

	"github.com/hashicorp/go-memdb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/mitchellh/devflow/internal/server/gen"
)

const (
	instanceExecTableName           = "instance-execs"
	instanceExecIdIndexName         = "id"
	instanceExecInstanceIdIndexName = "deployment-id"
)

func init() {
	schemas = append(schemas, instanceExecSchema)
}

var instanceExecId int64

func instanceExecSchema() *memdb.TableSchema {
	return &memdb.TableSchema{
		Name: instanceExecTableName,
		Indexes: map[string]*memdb.IndexSchema{
			instanceExecIdIndexName: &memdb.IndexSchema{
				Name:         instanceExecIdIndexName,
				AllowMissing: false,
				Unique:       true,
				Indexer: &memdb.IntFieldIndex{
					Field: "Id",
				},
			},

			instanceExecInstanceIdIndexName: &memdb.IndexSchema{
				Name:         instanceExecInstanceIdIndexName,
				AllowMissing: false,
				Unique:       false,
				Indexer: &memdb.StringFieldIndex{
					Field:     "InstanceId",
					Lowercase: true,
				},
			},
		},
	}
}

type InstanceExec struct {
	Id         int64
	InstanceId string
	Args       []string
	Reader     io.Reader
	EventCh    chan<- *pb.EntrypointExecRequest
	Connected  uint32
}

func (s *State) InstanceExecCreateByDeployment(did string, exec *InstanceExec) error {
	txn := s.inmem.Txn(true)
	defer txn.Abort()

	// Find all the instances by deployment
	iter, err := txn.Get(instanceTableName, instanceDeploymentIdIndexName, did)
	if err != nil {
		return status.Errorf(codes.Aborted, err.Error())
	}

	// Go through each to try to find the least loaded. Most likely there
	// will be an instance with no exec sessions and we prefer that.
	var min *Instance
	minCount := 0
	for raw := iter.Next(); raw != nil; raw = iter.Next() {
		rec := raw.(*Instance)

		execs, err := s.instanceExecListByInstanceId(txn, rec.Id)
		if err != nil {
			return err
		}

		// Zero length exec means we take it right away
		if len(execs) == 0 {
			min = rec
			break
		}

		// Otherwise we keep track of the lowest "load" exec which we just
		// choose by the minimum number of registered sessions.
		if min == nil || len(execs) < minCount {
			min = rec
			minCount = len(execs)
		}
	}

	if min == nil {
		return status.Errorf(codes.ResourceExhausted,
			"No available instances for exec.")
	}

	// Set the instance ID that we'll be using
	exec.InstanceId = min.Id

	// Set our ID
	exec.Id = atomic.AddInt64(&instanceExecId, 1)

	// Insert
	if err := txn.Insert(instanceExecTableName, exec); err != nil {
		return status.Errorf(codes.Aborted, err.Error())
	}
	txn.Commit()

	return nil
}

func (s *State) InstanceExecDelete(id int64) error {
	txn := s.inmem.Txn(true)
	defer txn.Abort()
	if _, err := txn.DeleteAll(instanceExecTableName, instanceExecIdIndexName, id); err != nil {
		return status.Errorf(codes.Aborted, err.Error())
	}
	txn.Commit()

	return nil
}

func (s *State) instanceExecListByInstanceId(txn *memdb.Txn, id string) ([]*InstanceExec, error) {
	// Find all the exec sessions
	iter, err := txn.Get(instanceExecTableName, instanceExecInstanceIdIndexName, id)
	if err != nil {
		return nil, status.Errorf(codes.Aborted, err.Error())
	}

	var result []*InstanceExec
	for raw := iter.Next(); raw != nil; raw = iter.Next() {
		result = append(result, raw.(*InstanceExec))
	}

	return result, nil
}