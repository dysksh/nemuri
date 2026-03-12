package state

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	// HeartbeatExpiry is the duration after which a heartbeat is considered stale.
	HeartbeatExpiry = 10 * time.Minute

	// JobTTL is the duration after which a job record is eligible for DynamoDB TTL deletion.
	JobTTL = 30 * 24 * time.Hour
)

// Job represents a job record in DynamoDB.
type Job struct {
	JobID            string `dynamodbav:"job_id"`
	ThreadID         string `dynamodbav:"thread_id,omitempty"`
	ChannelID        string `dynamodbav:"channel_id"`
	RequestUserID    string `dynamodbav:"request_user_id,omitempty"`
	InteractionToken string `dynamodbav:"interaction_token"`
	ApplicationID    string `dynamodbav:"application_id"`

	State    JobState `dynamodbav:"state"`
	Step     string   `dynamodbav:"step,omitempty"`
	Revision int      `dynamodbav:"revision"`

	WorkerID    string `dynamodbav:"worker_id,omitempty"`
	HeartbeatAt int64  `dynamodbav:"heartbeat_at,omitempty"`
	Version     int    `dynamodbav:"version"`

	Prompt       string `dynamodbav:"prompt"`
	UserResponse string `dynamodbav:"user_response,omitempty"`
	ErrorMessage string `dynamodbav:"error_message,omitempty"`

	CreatedAt int64 `dynamodbav:"created_at"`
	UpdatedAt int64 `dynamodbav:"updated_at"`
	TTL       int64 `dynamodbav:"ttl"`
}

// DynamoDBAPI is the subset of the DynamoDB client used by Store.
type DynamoDBAPI interface {
	PutItem(ctx context.Context, params *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	GetItem(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	UpdateItem(ctx context.Context, params *dynamodb.UpdateItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	Query(ctx context.Context, params *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

// Store manages job state in DynamoDB.
type Store struct {
	client    DynamoDBAPI
	tableName string
}

// NewStore creates a new state Store.
func NewStore(client DynamoDBAPI, tableName string) *Store {
	return &Store{
		client:    client,
		tableName: tableName,
	}
}

// CreateJobInput holds the parameters for creating a new job.
type CreateJobInput struct {
	JobID            string
	Prompt           string
	ChannelID        string
	InteractionToken string
	ApplicationID    string
}

// CreateJob creates a new job record in DynamoDB with state=INIT.
func (s *Store) CreateJob(ctx context.Context, input CreateJobInput) error {
	now := time.Now().Unix()
	ttl := now + int64(JobTTL.Seconds())

	item, err := attributevalue.MarshalMap(Job{
		JobID:            input.JobID,
		State:            StateInit,
		Prompt:           input.Prompt,
		ChannelID:        input.ChannelID,
		InteractionToken: input.InteractionToken,
		ApplicationID:    input.ApplicationID,
		Version:          0,
		Revision:         0,
		CreatedAt:        now,
		UpdatedAt:        now,
		TTL:              ttl,
	})
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(s.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(job_id)"),
	})
	if err != nil {
		return fmt.Errorf("create job %s: %w", input.JobID, err)
	}
	return nil
}

// GetJob retrieves a job by ID.
func (s *Store) GetJob(ctx context.Context, jobID string) (*Job, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"job_id": &types.AttributeValueMemberS{Value: jobID},
		},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("GetItem: %w", err)
	}
	if out.Item == nil {
		return nil, fmt.Errorf("job %s not found", jobID)
	}

	var job Job
	if err := attributevalue.UnmarshalMap(out.Item, &job); err != nil {
		return nil, fmt.Errorf("unmarshal job: %w", err)
	}
	return &job, nil
}

// AcquireLock attempts to acquire the execution lock for a job.
// It succeeds only if:
//   - state is INIT, FAILED, or WAITING_USER_INPUT
//   - worker_id is not set OR heartbeat_at is expired
func (s *Store) AcquireLock(ctx context.Context, jobID, workerID string) error {
	now := time.Now().Unix()
	expired := now - int64(HeartbeatExpiry.Seconds())

	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"job_id": &types.AttributeValueMemberS{Value: jobID},
		},
		UpdateExpression: aws.String("SET #state = :running, worker_id = :wid, heartbeat_at = :now, updated_at = :now, version = version + :one"),
		ConditionExpression: aws.String(
			"(#state IN (:init, :failed, :waiting)) AND (attribute_not_exists(worker_id) OR worker_id = :empty OR heartbeat_at < :expired)",
		),
		ExpressionAttributeNames: map[string]string{
			"#state": "state",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":running": &types.AttributeValueMemberS{Value: string(StateRunning)},
			":init":    &types.AttributeValueMemberS{Value: string(StateInit)},
			":failed":  &types.AttributeValueMemberS{Value: string(StateFailed)},
			":waiting": &types.AttributeValueMemberS{Value: string(StateWaitingUserInput)},
			":wid":     &types.AttributeValueMemberS{Value: workerID},
			":empty":   &types.AttributeValueMemberS{Value: ""},
			":now":     &types.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)},
			":expired": &types.AttributeValueMemberN{Value: strconv.FormatInt(expired, 10)},
			":one":     &types.AttributeValueMemberN{Value: "1"},
		},
	})
	if err != nil {
		return fmt.Errorf("acquire lock for job %s: %w", jobID, err)
	}
	return nil
}

// Heartbeat updates the heartbeat timestamp for the job.
// Only succeeds if the current worker still holds the lock.
func (s *Store) Heartbeat(ctx context.Context, jobID, workerID string) error {
	now := time.Now().Unix()

	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"job_id": &types.AttributeValueMemberS{Value: jobID},
		},
		UpdateExpression:    aws.String("SET heartbeat_at = :now, updated_at = :now"),
		ConditionExpression: aws.String("worker_id = :wid"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":wid": &types.AttributeValueMemberS{Value: workerID},
			":now": &types.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)},
		},
	})
	if err != nil {
		return fmt.Errorf("heartbeat for job %s: %w", jobID, err)
	}
	return nil
}

// TransitionState changes the job state, validating the transition.
// Only succeeds if the current worker holds the lock and the version matches.
func (s *Store) TransitionState(ctx context.Context, jobID, workerID string, currentVersion int, from, to JobState) error {
	if err := ValidateTransition(from, to); err != nil {
		return err
	}

	now := time.Now().Unix()

	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"job_id": &types.AttributeValueMemberS{Value: jobID},
		},
		UpdateExpression:    aws.String("SET #state = :new_state, version = :new_version, updated_at = :now"),
		ConditionExpression: aws.String("worker_id = :wid AND version = :v"),
		ExpressionAttributeNames: map[string]string{
			"#state": "state",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":new_state":   &types.AttributeValueMemberS{Value: string(to)},
			":new_version": &types.AttributeValueMemberN{Value: strconv.Itoa(currentVersion + 1)},
			":now":         &types.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)},
			":wid":         &types.AttributeValueMemberS{Value: workerID},
			":v":           &types.AttributeValueMemberN{Value: strconv.Itoa(currentVersion)},
		},
	})
	if err != nil {
		return fmt.Errorf("transition job %s from %s to %s: %w", jobID, from, to, err)
	}
	return nil
}

// MarkDone transitions the job to DONE and removes the worker_id.
func (s *Store) MarkDone(ctx context.Context, jobID, workerID string, currentVersion int, from JobState) error {
	if err := ValidateTransition(from, StateDone); err != nil {
		return err
	}

	now := time.Now().Unix()

	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"job_id": &types.AttributeValueMemberS{Value: jobID},
		},
		UpdateExpression:    aws.String("SET #state = :done, version = :new_version, updated_at = :now REMOVE worker_id, heartbeat_at"),
		ConditionExpression: aws.String("worker_id = :wid"),
		ExpressionAttributeNames: map[string]string{
			"#state": "state",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":done":        &types.AttributeValueMemberS{Value: string(StateDone)},
			":new_version": &types.AttributeValueMemberN{Value: strconv.Itoa(currentVersion + 1)},
			":now":         &types.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)},
			":wid":         &types.AttributeValueMemberS{Value: workerID},
		},
	})
	if err != nil {
		return fmt.Errorf("mark done for job %s: %w", jobID, err)
	}
	return nil
}

// MarkWaitingUserInput transitions the job to WAITING_USER_INPUT, sets thread_id,
// and removes worker_id/heartbeat_at so the next ECS task can acquire the lock.
func (s *Store) MarkWaitingUserInput(ctx context.Context, jobID, workerID string, currentVersion int, threadID string) error {
	if err := ValidateTransition(StateRunning, StateWaitingUserInput); err != nil {
		return err
	}

	now := time.Now().Unix()

	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"job_id": &types.AttributeValueMemberS{Value: jobID},
		},
		UpdateExpression:    aws.String("SET #state = :waiting, thread_id = :tid, version = :new_version, updated_at = :now REMOVE worker_id, heartbeat_at"),
		ConditionExpression: aws.String("worker_id = :wid AND version = :v"),
		ExpressionAttributeNames: map[string]string{
			"#state": "state",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":waiting":     &types.AttributeValueMemberS{Value: string(StateWaitingUserInput)},
			":tid":         &types.AttributeValueMemberS{Value: threadID},
			":new_version": &types.AttributeValueMemberN{Value: strconv.Itoa(currentVersion + 1)},
			":now":         &types.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)},
			":wid":         &types.AttributeValueMemberS{Value: workerID},
			":v":           &types.AttributeValueMemberN{Value: strconv.Itoa(currentVersion)},
		},
	})
	if err != nil {
		return fmt.Errorf("mark waiting user input for job %s: %w", jobID, err)
	}
	return nil
}

// MarkWaitingApproval transitions the job to WAITING_APPROVAL via READY_FOR_PR,
// and removes worker_id/heartbeat_at.
func (s *Store) MarkWaitingApproval(ctx context.Context, jobID, workerID string, currentVersion int, threadID string) error {
	now := time.Now().Unix()

	updateExpr := "SET #state = :approval, version = :new_version, updated_at = :now REMOVE worker_id, heartbeat_at"
	exprValues := map[string]types.AttributeValue{
		":approval":    &types.AttributeValueMemberS{Value: string(StateWaitingApproval)},
		":new_version": &types.AttributeValueMemberN{Value: strconv.Itoa(currentVersion + 1)},
		":now":         &types.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)},
		":wid":         &types.AttributeValueMemberS{Value: workerID},
		":v":           &types.AttributeValueMemberN{Value: strconv.Itoa(currentVersion)},
	}

	if threadID != "" {
		updateExpr = "SET #state = :approval, thread_id = :tid, version = :new_version, updated_at = :now REMOVE worker_id, heartbeat_at"
		exprValues[":tid"] = &types.AttributeValueMemberS{Value: threadID}
	}

	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"job_id": &types.AttributeValueMemberS{Value: jobID},
		},
		UpdateExpression:    aws.String(updateExpr),
		ConditionExpression: aws.String("worker_id = :wid AND version = :v"),
		ExpressionAttributeNames: map[string]string{
			"#state": "state",
		},
		ExpressionAttributeValues: exprValues,
	})
	if err != nil {
		return fmt.Errorf("mark waiting approval for job %s: %w", jobID, err)
	}
	return nil
}

// SetUserResponse saves the user's response and the new interaction token on a WAITING_USER_INPUT job.
// The interaction token is updated so the ECS task can follow up the resume command's deferred ACK.
// This is called by the Ingress Lambda before enqueuing the resume message.
func (s *Store) SetUserResponse(ctx context.Context, jobID, userResponse, interactionToken string) error {
	now := time.Now().Unix()

	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"job_id": &types.AttributeValueMemberS{Value: jobID},
		},
		UpdateExpression:    aws.String("SET user_response = :resp, interaction_token = :token, updated_at = :now"),
		ConditionExpression: aws.String("#state = :waiting"),
		ExpressionAttributeNames: map[string]string{
			"#state": "state",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":resp":    &types.AttributeValueMemberS{Value: userResponse},
			":token":   &types.AttributeValueMemberS{Value: interactionToken},
			":waiting": &types.AttributeValueMemberS{Value: string(StateWaitingUserInput)},
			":now":     &types.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)},
		},
	})
	if err != nil {
		return fmt.Errorf("set user response for job %s: %w", jobID, err)
	}
	return nil
}

// ApproveJob transitions a WAITING_APPROVAL job to DONE.
// This is called by the Ingress Lambda when the user approves.
func (s *Store) ApproveJob(ctx context.Context, jobID string) error {
	now := time.Now().Unix()

	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"job_id": &types.AttributeValueMemberS{Value: jobID},
		},
		UpdateExpression:    aws.String("SET #state = :done, version = version + :one, updated_at = :now"),
		ConditionExpression: aws.String("#state = :approval"),
		ExpressionAttributeNames: map[string]string{
			"#state": "state",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":done":     &types.AttributeValueMemberS{Value: string(StateDone)},
			":approval": &types.AttributeValueMemberS{Value: string(StateWaitingApproval)},
			":one":      &types.AttributeValueMemberN{Value: "1"},
			":now":      &types.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)},
		},
	})
	if err != nil {
		return fmt.Errorf("approve job %s: %w", jobID, err)
	}
	return nil
}

// QueryByThreadID looks up a job by Discord thread ID using the GSI.
// Returns nil if no job is found for the given thread_id.
func (s *Store) QueryByThreadID(ctx context.Context, threadID string) (*Job, error) {
	out, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		IndexName:              aws.String("thread_id-index"),
		KeyConditionExpression: aws.String("thread_id = :tid"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":tid": &types.AttributeValueMemberS{Value: threadID},
		},
		Limit: aws.Int32(1),
	})
	if err != nil {
		return nil, fmt.Errorf("query by thread_id %s: %w", threadID, err)
	}
	if len(out.Items) == 0 {
		return nil, nil
	}

	var job Job
	if err := attributevalue.UnmarshalMap(out.Items[0], &job); err != nil {
		return nil, fmt.Errorf("unmarshal job: %w", err)
	}
	return &job, nil
}

// MarkFailed transitions the job to FAILED and removes the worker_id.
func (s *Store) MarkFailed(ctx context.Context, jobID, workerID, errorMessage string, currentVersion int, from JobState) error {
	if err := ValidateTransition(from, StateFailed); err != nil {
		return err
	}

	now := time.Now().Unix()

	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"job_id": &types.AttributeValueMemberS{Value: jobID},
		},
		UpdateExpression:    aws.String("SET #state = :failed, error_message = :err, version = :new_version, updated_at = :now REMOVE worker_id, heartbeat_at"),
		ConditionExpression: aws.String("worker_id = :wid"),
		ExpressionAttributeNames: map[string]string{
			"#state": "state",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":failed":      &types.AttributeValueMemberS{Value: string(StateFailed)},
			":err":         &types.AttributeValueMemberS{Value: errorMessage},
			":new_version": &types.AttributeValueMemberN{Value: strconv.Itoa(currentVersion + 1)},
			":now":         &types.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)},
			":wid":         &types.AttributeValueMemberS{Value: workerID},
		},
	})
	if err != nil {
		return fmt.Errorf("mark failed for job %s: %w", jobID, err)
	}
	return nil
}
