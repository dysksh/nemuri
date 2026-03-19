package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecsTypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// mockECSClient implements ecsRunTaskAPI for testing.
type mockECSClient struct {
	runTaskFunc func(ctx context.Context, params *ecs.RunTaskInput, optFns ...func(*ecs.Options)) (*ecs.RunTaskOutput, error)
}

func (m *mockECSClient) RunTask(ctx context.Context, params *ecs.RunTaskInput, optFns ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	if m.runTaskFunc != nil {
		return m.runTaskFunc(ctx, params, optFns...)
	}
	return &ecs.RunTaskOutput{
		Tasks: []ecsTypes.Task{{TaskArn: aws.String("arn:aws:ecs:ap-northeast-1:000000000000:task/test/abc123")}},
	}, nil
}

func TestSQSJobMessageParsing(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    sqsJobMessage
		wantErr bool
	}{
		{
			name:  "valid message",
			input: `{"job_id":"abc-123","prompt":"do something","interaction_token":"tok","channel_id":"ch-1","application_id":"app-1"}`,
			want: sqsJobMessage{
				JobID:            "abc-123",
				Prompt:           "do something",
				InteractionToken: "tok",
				ChannelID:        "ch-1",
				ApplicationID:    "app-1",
			},
		},
		{
			name:  "minimal message",
			input: `{"job_id":"id-1"}`,
			want:  sqsJobMessage{JobID: "id-1"},
		},
		{
			name:    "invalid JSON",
			input:   `{invalid`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg sqsJobMessage
			err := json.Unmarshal([]byte(tt.input), &msg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Unmarshal() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if msg != tt.want {
				t.Errorf("got %+v, want %+v", msg, tt.want)
			}
		})
	}
}

func TestProcessRecord(t *testing.T) {
	// Save and restore globals
	origClient := ecsClient
	origCluster := clusterArn
	origTaskDef := taskDefinitionArn
	origSubnets := subnetIDs
	origSG := securityGroupID
	t.Cleanup(func() {
		ecsClient = origClient
		clusterArn = origCluster
		taskDefinitionArn = origTaskDef
		subnetIDs = origSubnets
		securityGroupID = origSG
	})

	clusterArn = "arn:aws:ecs:ap-northeast-1:000000000000:cluster/test"
	taskDefinitionArn = "arn:aws:ecs:ap-northeast-1:000000000000:task-definition/test:1"
	subnetIDs = []string{"subnet-aaa", "subnet-bbb"}
	securityGroupID = "sg-test"

	t.Run("success", func(t *testing.T) {
		var capturedInput *ecs.RunTaskInput
		ecsClient = &mockECSClient{
			runTaskFunc: func(_ context.Context, params *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
				capturedInput = params
				return &ecs.RunTaskOutput{
					Tasks: []ecsTypes.Task{{TaskArn: aws.String("arn:aws:ecs:ap-northeast-1:000000000000:task/test/abc123")}},
				}, nil
			},
		}

		record := events.SQSMessage{
			Body:          `{"job_id":"job-1","prompt":"test prompt"}`,
			ReceiptHandle: "receipt-1",
		}

		err := processRecord(context.Background(), record)
		if err != nil {
			t.Fatalf("processRecord() error = %v", err)
		}

		if capturedInput == nil {
			t.Fatal("RunTask was not called")
		}
		if *capturedInput.Cluster != clusterArn {
			t.Errorf("cluster = %q, want %q", *capturedInput.Cluster, clusterArn)
		}
		if *capturedInput.TaskDefinition != taskDefinitionArn {
			t.Errorf("task definition = %q, want %q", *capturedInput.TaskDefinition, taskDefinitionArn)
		}

		overrides := capturedInput.Overrides.ContainerOverrides[0]
		envMap := make(map[string]string)
		for _, kv := range overrides.Environment {
			envMap[*kv.Name] = *kv.Value
		}
		if envMap["JOB_ID"] != "job-1" {
			t.Errorf("JOB_ID = %q, want %q", envMap["JOB_ID"], "job-1")
		}
	})

	t.Run("invalid JSON body", func(t *testing.T) {
		ecsClient = &mockECSClient{}
		record := events.SQSMessage{Body: `{invalid`}
		err := processRecord(context.Background(), record)
		if err == nil {
			t.Fatal("processRecord() expected error for invalid JSON")
		}
	})

	t.Run("RunTask API error", func(t *testing.T) {
		ecsClient = &mockECSClient{
			runTaskFunc: func(_ context.Context, _ *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
				return nil, fmt.Errorf("ECS service unavailable")
			},
		}

		record := events.SQSMessage{Body: `{"job_id":"job-1"}`}
		err := processRecord(context.Background(), record)
		if err == nil {
			t.Fatal("processRecord() expected error on RunTask failure")
		}
	})

	t.Run("RunTask returns failure", func(t *testing.T) {
		ecsClient = &mockECSClient{
			runTaskFunc: func(_ context.Context, _ *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
				return &ecs.RunTaskOutput{
					Failures: []ecsTypes.Failure{
						{Reason: aws.String("RESOURCE"), Detail: aws.String("no capacity")},
					},
				}, nil
			},
		}

		record := events.SQSMessage{Body: `{"job_id":"job-1"}`}
		err := processRecord(context.Background(), record)
		if err == nil {
			t.Fatal("processRecord() expected error on RunTask failure response")
		}
	})
}

func TestHandler(t *testing.T) {
	origClient := ecsClient
	origCluster := clusterArn
	origTaskDef := taskDefinitionArn
	origSubnets := subnetIDs
	origSG := securityGroupID
	t.Cleanup(func() {
		ecsClient = origClient
		clusterArn = origCluster
		taskDefinitionArn = origTaskDef
		subnetIDs = origSubnets
		securityGroupID = origSG
	})

	clusterArn = "arn:aws:ecs:ap-northeast-1:000000000000:cluster/test"
	taskDefinitionArn = "arn:aws:ecs:ap-northeast-1:000000000000:task-definition/test:1"
	subnetIDs = []string{"subnet-aaa"}
	securityGroupID = "sg-test"
	ecsClient = &mockECSClient{}

	t.Run("processes all records", func(t *testing.T) {
		sqsEvent := events.SQSEvent{
			Records: []events.SQSMessage{
				{Body: `{"job_id":"job-1"}`, ReceiptHandle: "r1"},
				{Body: `{"job_id":"job-2"}`, ReceiptHandle: "r2"},
			},
		}
		err := handler(context.Background(), sqsEvent)
		if err != nil {
			t.Fatalf("handler() error = %v", err)
		}
	})

	t.Run("fails on bad record", func(t *testing.T) {
		sqsEvent := events.SQSEvent{
			Records: []events.SQSMessage{
				{Body: `{invalid`},
			},
		}
		err := handler(context.Background(), sqsEvent)
		if err == nil {
			t.Fatal("handler() expected error for invalid record")
		}
	})
}
