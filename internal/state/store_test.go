package state_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/nemuri/nemuri/internal/state"
)

// mockDynamoDB implements state.DynamoDBAPI for testing.
type mockDynamoDB struct {
	putItemFunc    func(ctx context.Context, params *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	getItemFunc    func(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	updateItemFunc func(ctx context.Context, params *dynamodb.UpdateItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	queryFunc      func(ctx context.Context, params *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

func (m *mockDynamoDB) PutItem(ctx context.Context, params *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	if m.putItemFunc != nil {
		return m.putItemFunc(ctx, params, optFns...)
	}
	return &dynamodb.PutItemOutput{}, nil
}

func (m *mockDynamoDB) GetItem(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	if m.getItemFunc != nil {
		return m.getItemFunc(ctx, params, optFns...)
	}
	return &dynamodb.GetItemOutput{}, nil
}

func (m *mockDynamoDB) UpdateItem(ctx context.Context, params *dynamodb.UpdateItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	if m.updateItemFunc != nil {
		return m.updateItemFunc(ctx, params, optFns...)
	}
	return &dynamodb.UpdateItemOutput{}, nil
}

func (m *mockDynamoDB) Query(ctx context.Context, params *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	if m.queryFunc != nil {
		return m.queryFunc(ctx, params, optFns...)
	}
	return &dynamodb.QueryOutput{}, nil
}

func TestCreateJob(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mock := &mockDynamoDB{
			putItemFunc: func(_ context.Context, params *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
				if *params.TableName != "test-jobs" {
					t.Errorf("table name = %q, want %q", *params.TableName, "test-jobs")
				}
				jobID, ok := params.Item["job_id"].(*types.AttributeValueMemberS)
				if !ok || jobID.Value != "job-1" {
					t.Errorf("job_id = %v, want %q", params.Item["job_id"], "job-1")
				}
				st, ok := params.Item["state"].(*types.AttributeValueMemberS)
				if !ok || st.Value != "INIT" {
					t.Errorf("state = %v, want %q", params.Item["state"], "INIT")
				}
				return &dynamodb.PutItemOutput{}, nil
			},
		}

		store := state.NewStore(mock, "test-jobs")
		err := store.CreateJob(context.Background(), state.CreateJobInput{
			JobID:            "job-1",
			Prompt:           "test prompt",
			ChannelID:        "ch-1",
			InteractionToken: "token-1",
			ApplicationID:    "app-1",
		})
		if err != nil {
			t.Fatalf("CreateJob() error = %v", err)
		}
	})

	t.Run("conditional check failure", func(t *testing.T) {
		mock := &mockDynamoDB{
			putItemFunc: func(_ context.Context, _ *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
				return nil, &types.ConditionalCheckFailedException{Message: strPtr("already exists")}
			},
		}

		store := state.NewStore(mock, "test-jobs")
		err := store.CreateJob(context.Background(), state.CreateJobInput{JobID: "job-1"})
		if err == nil {
			t.Fatal("CreateJob() expected error for duplicate job")
		}
	})
}

func TestGetJob(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mock := &mockDynamoDB{
			getItemFunc: func(_ context.Context, params *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
				return &dynamodb.GetItemOutput{
					Item: map[string]types.AttributeValue{
						"job_id":            &types.AttributeValueMemberS{Value: "job-1"},
						"state":             &types.AttributeValueMemberS{Value: "INIT"},
						"prompt":            &types.AttributeValueMemberS{Value: "test"},
						"channel_id":        &types.AttributeValueMemberS{Value: "ch-1"},
						"interaction_token": &types.AttributeValueMemberS{Value: "tok-1"},
						"application_id":    &types.AttributeValueMemberS{Value: "app-1"},
						"version":           &types.AttributeValueMemberN{Value: "0"},
						"revision":          &types.AttributeValueMemberN{Value: "0"},
						"created_at":        &types.AttributeValueMemberN{Value: "1700000000"},
						"updated_at":        &types.AttributeValueMemberN{Value: "1700000000"},
						"ttl":               &types.AttributeValueMemberN{Value: "1702592000"},
					},
				}, nil
			},
		}

		store := state.NewStore(mock, "test-jobs")
		job, err := store.GetJob(context.Background(), "job-1")
		if err != nil {
			t.Fatalf("GetJob() error = %v", err)
		}
		if job.JobID != "job-1" {
			t.Errorf("job.JobID = %q, want %q", job.JobID, "job-1")
		}
		if job.State != state.StateInit {
			t.Errorf("job.State = %q, want %q", job.State, state.StateInit)
		}
	})

	t.Run("not found", func(t *testing.T) {
		mock := &mockDynamoDB{
			getItemFunc: func(_ context.Context, _ *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
				return &dynamodb.GetItemOutput{Item: nil}, nil
			},
		}

		store := state.NewStore(mock, "test-jobs")
		_, err := store.GetJob(context.Background(), "nonexistent")
		if err == nil {
			t.Fatal("GetJob() expected error for nonexistent job")
		}
	})
}

func TestAcquireLock(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mock := &mockDynamoDB{
			updateItemFunc: func(_ context.Context, params *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
				wid := params.ExpressionAttributeValues[":wid"].(*types.AttributeValueMemberS)
				if wid.Value != "worker-1" {
					t.Errorf("worker_id = %q, want %q", wid.Value, "worker-1")
				}
				return &dynamodb.UpdateItemOutput{}, nil
			},
		}

		store := state.NewStore(mock, "test-jobs")
		err := store.AcquireLock(context.Background(), "job-1", "worker-1")
		if err != nil {
			t.Fatalf("AcquireLock() error = %v", err)
		}
	})

	t.Run("lock already held", func(t *testing.T) {
		mock := &mockDynamoDB{
			updateItemFunc: func(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
				return nil, &types.ConditionalCheckFailedException{Message: strPtr("condition failed")}
			},
		}

		store := state.NewStore(mock, "test-jobs")
		err := store.AcquireLock(context.Background(), "job-1", "worker-2")
		if err == nil {
			t.Fatal("AcquireLock() expected error when lock already held")
		}
	})
}

func TestTransitionState(t *testing.T) {
	t.Run("valid transition", func(t *testing.T) {
		mock := &mockDynamoDB{
			updateItemFunc: func(_ context.Context, params *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
				newState := params.ExpressionAttributeValues[":new_state"].(*types.AttributeValueMemberS)
				if newState.Value != "DONE" {
					t.Errorf("new state = %q, want %q", newState.Value, "DONE")
				}
				return &dynamodb.UpdateItemOutput{}, nil
			},
		}

		store := state.NewStore(mock, "test-jobs")
		err := store.TransitionState(context.Background(), "job-1", "worker-1", 1, state.StateRunning, state.StateDone)
		if err != nil {
			t.Fatalf("TransitionState() error = %v", err)
		}
	})

	t.Run("invalid transition rejected before DynamoDB call", func(t *testing.T) {
		mock := &mockDynamoDB{
			updateItemFunc: func(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
				t.Fatal("DynamoDB should not be called for invalid transition")
				return nil, nil
			},
		}

		store := state.NewStore(mock, "test-jobs")
		err := store.TransitionState(context.Background(), "job-1", "worker-1", 0, state.StateInit, state.StateDone)
		if err == nil {
			t.Fatal("TransitionState() expected error for INIT→DONE")
		}
	})

	t.Run("version conflict", func(t *testing.T) {
		mock := &mockDynamoDB{
			updateItemFunc: func(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
				return nil, &types.ConditionalCheckFailedException{Message: strPtr("condition failed")}
			},
		}

		store := state.NewStore(mock, "test-jobs")
		err := store.TransitionState(context.Background(), "job-1", "worker-1", 0, state.StateRunning, state.StateDone)
		if err == nil {
			t.Fatal("TransitionState() expected error on version conflict")
		}
	})
}

func TestHeartbeat(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var capturedParams *dynamodb.UpdateItemInput
		mock := &mockDynamoDB{
			updateItemFunc: func(_ context.Context, params *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
				capturedParams = params
				return &dynamodb.UpdateItemOutput{}, nil
			},
		}

		store := state.NewStore(mock, "test-jobs")
		err := store.Heartbeat(context.Background(), "job-1", "worker-1")
		if err != nil {
			t.Fatalf("Heartbeat() error = %v", err)
		}

		// Verify condition checks worker_id
		wid := capturedParams.ExpressionAttributeValues[":wid"].(*types.AttributeValueMemberS)
		if wid.Value != "worker-1" {
			t.Errorf("worker_id = %q, want %q", wid.Value, "worker-1")
		}
	})

	t.Run("lock not held", func(t *testing.T) {
		mock := &mockDynamoDB{
			updateItemFunc: func(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
				return nil, &types.ConditionalCheckFailedException{Message: strPtr("condition failed")}
			},
		}

		store := state.NewStore(mock, "test-jobs")
		err := store.Heartbeat(context.Background(), "job-1", "worker-1")
		if err == nil {
			t.Fatal("Heartbeat() expected error when lock not held")
		}
	})
}

func TestMarkDone(t *testing.T) {
	t.Run("success from RUNNING", func(t *testing.T) {
		var capturedParams *dynamodb.UpdateItemInput
		mock := &mockDynamoDB{
			updateItemFunc: func(_ context.Context, params *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
				capturedParams = params
				return &dynamodb.UpdateItemOutput{}, nil
			},
		}
		store := state.NewStore(mock, "test-jobs")
		err := store.MarkDone(context.Background(), "job-1", "worker-1", 1, state.StateRunning)
		if err != nil {
			t.Fatalf("MarkDone() error = %v", err)
		}

		// Verify state is set to DONE
		doneVal := capturedParams.ExpressionAttributeValues[":done"].(*types.AttributeValueMemberS)
		if doneVal.Value != string(state.StateDone) {
			t.Errorf("state = %q, want %q", doneVal.Value, state.StateDone)
		}
		// Verify worker_id condition
		wid := capturedParams.ExpressionAttributeValues[":wid"].(*types.AttributeValueMemberS)
		if wid.Value != "worker-1" {
			t.Errorf("worker_id = %q, want %q", wid.Value, "worker-1")
		}
		// Verify version is incremented
		ver := capturedParams.ExpressionAttributeValues[":new_version"].(*types.AttributeValueMemberN)
		if ver.Value != "2" {
			t.Errorf("new_version = %q, want %q", ver.Value, "2")
		}
	})

	t.Run("invalid from INIT", func(t *testing.T) {
		mock := &mockDynamoDB{
			updateItemFunc: func(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
				t.Fatal("DynamoDB should not be called for invalid transition")
				return nil, nil
			},
		}
		store := state.NewStore(mock, "test-jobs")
		err := store.MarkDone(context.Background(), "job-1", "worker-1", 0, state.StateInit)
		if err == nil {
			t.Fatal("MarkDone() expected error for INIT→DONE")
		}
	})
}

func TestMarkFailed(t *testing.T) {
	t.Run("success from RUNNING", func(t *testing.T) {
		var capturedParams *dynamodb.UpdateItemInput
		mock := &mockDynamoDB{
			updateItemFunc: func(_ context.Context, params *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
				capturedParams = params
				return &dynamodb.UpdateItemOutput{}, nil
			},
		}
		store := state.NewStore(mock, "test-jobs")
		err := store.MarkFailed(context.Background(), "job-1", "worker-1", "something broke", 1, state.StateRunning)
		if err != nil {
			t.Fatalf("MarkFailed() error = %v", err)
		}

		// Verify state is set to FAILED
		failedVal := capturedParams.ExpressionAttributeValues[":failed"].(*types.AttributeValueMemberS)
		if failedVal.Value != string(state.StateFailed) {
			t.Errorf("state = %q, want %q", failedVal.Value, state.StateFailed)
		}
		// Verify error message is passed
		errMsg := capturedParams.ExpressionAttributeValues[":err"].(*types.AttributeValueMemberS)
		if errMsg.Value != "something broke" {
			t.Errorf("error_message = %q, want %q", errMsg.Value, "something broke")
		}
		// Verify version is incremented
		ver := capturedParams.ExpressionAttributeValues[":new_version"].(*types.AttributeValueMemberN)
		if ver.Value != "2" {
			t.Errorf("new_version = %q, want %q", ver.Value, "2")
		}
	})

	t.Run("invalid from INIT", func(t *testing.T) {
		mock := &mockDynamoDB{
			updateItemFunc: func(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
				t.Fatal("DynamoDB should not be called for invalid transition")
				return nil, nil
			},
		}
		store := state.NewStore(mock, "test-jobs")
		err := store.MarkFailed(context.Background(), "job-1", "worker-1", "err", 0, state.StateInit)
		if err == nil {
			t.Fatal("MarkFailed() expected error for INIT→FAILED")
		}
	})

	t.Run("dynamo error", func(t *testing.T) {
		mock := &mockDynamoDB{
			updateItemFunc: func(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
				return nil, fmt.Errorf("connection refused")
			},
		}
		store := state.NewStore(mock, "test-jobs")
		err := store.MarkFailed(context.Background(), "job-1", "worker-1", "err", 1, state.StateRunning)
		if err == nil {
			t.Fatal("MarkFailed() expected error on DynamoDB failure")
		}
	})
}

func strPtr(s string) *string { return &s }
