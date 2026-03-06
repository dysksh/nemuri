package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecsTypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

type sqsJobMessage struct {
	JobID            string `json:"job_id"`
	Prompt           string `json:"prompt"`
	InteractionToken string `json:"interaction_token"`
	ChannelID        string `json:"channel_id"`
	ApplicationID    string `json:"application_id"`
}

var (
	ecsClient          *ecs.Client
	clusterArn         string
	taskDefinitionArn  string
	subnetIDs          []string
	securityGroupID    string
)

func init() {
	clusterArn = os.Getenv("ECS_CLUSTER_ARN")
	if clusterArn == "" {
		slog.Error("ECS_CLUSTER_ARN is not set")
		os.Exit(1)
	}

	taskDefinitionArn = os.Getenv("ECS_TASK_DEFINITION_ARN")
	if taskDefinitionArn == "" {
		slog.Error("ECS_TASK_DEFINITION_ARN is not set")
		os.Exit(1)
	}

	subnetsJSON := os.Getenv("ECS_SUBNET_IDS")
	if subnetsJSON == "" {
		slog.Error("ECS_SUBNET_IDS is not set")
		os.Exit(1)
	}
	if err := json.Unmarshal([]byte(subnetsJSON), &subnetIDs); err != nil {
		slog.Error("failed to parse ECS_SUBNET_IDS", "error", err)
		os.Exit(1)
	}

	securityGroupID = os.Getenv("ECS_SECURITY_GROUP_ID")
	if securityGroupID == "" {
		slog.Error("ECS_SECURITY_GROUP_ID is not set")
		os.Exit(1)
	}

	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		slog.Error("failed to load AWS config", "error", err)
		os.Exit(1)
	}
	ecsClient = ecs.NewFromConfig(cfg)
}

func handler(ctx context.Context, sqsEvent events.SQSEvent) error {
	for _, record := range sqsEvent.Records {
		if err := processRecord(ctx, record); err != nil {
			slog.Error("failed to process SQS record", "error", err, "message_id", record.MessageId)
			return err
		}
	}
	return nil
}

func processRecord(ctx context.Context, record events.SQSMessage) error {
	var msg sqsJobMessage
	if err := json.Unmarshal([]byte(record.Body), &msg); err != nil {
		return fmt.Errorf("unmarshal SQS message: %w", err)
	}

	slog.Info("launching ECS task", "job_id", msg.JobID)

	input := &ecs.RunTaskInput{
		Cluster:        aws.String(clusterArn),
		TaskDefinition: aws.String(taskDefinitionArn),
		LaunchType:     ecsTypes.LaunchTypeFargate,
		Count:          aws.Int32(1),
		NetworkConfiguration: &ecsTypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecsTypes.AwsVpcConfiguration{
				Subnets:        subnetIDs,
				SecurityGroups: []string{securityGroupID},
				AssignPublicIp: ecsTypes.AssignPublicIpEnabled,
			},
		},
		Overrides: &ecsTypes.TaskOverride{
			ContainerOverrides: []ecsTypes.ContainerOverride{
				{
					Name: aws.String("agent-engine"),
					Environment: []ecsTypes.KeyValuePair{
						{Name: aws.String("JOB_ID"), Value: aws.String(msg.JobID)},
						{Name: aws.String("SQS_RECEIPT_HANDLE"), Value: aws.String(record.ReceiptHandle)},
					},
				},
			},
		},
	}

	result, err := ecsClient.RunTask(ctx, input)
	if err != nil {
		return fmt.Errorf("RunTask: %w", err)
	}

	if len(result.Failures) > 0 {
		return fmt.Errorf("RunTask failure: %s - %s", aws.ToString(result.Failures[0].Reason), aws.ToString(result.Failures[0].Detail))
	}

	taskArn := aws.ToString(result.Tasks[0].TaskArn)
	slog.Info("ECS task launched", "job_id", msg.JobID, "task_arn", taskArn)
	return nil
}

func main() {
	lambda.Start(handler)
}
