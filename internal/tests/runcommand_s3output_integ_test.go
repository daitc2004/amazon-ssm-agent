// Copyright 2018 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may not
// use this file except in compliance with the License. A copy of the
// License is located at
//
// http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
// either express or implied. See the License for the specific language governing
// permissions and limitations under the License.

// Package tests represents stress and integration tests of the agent
package tests

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"runtime/debug"
	"testing"

	"github.com/aws/amazon-ssm-agent/agent/agent"
	"github.com/aws/amazon-ssm-agent/agent/appconfig"
	"github.com/aws/amazon-ssm-agent/agent/context"
	"github.com/aws/amazon-ssm-agent/agent/contracts"
	"github.com/aws/amazon-ssm-agent/agent/fileutil"
	"github.com/aws/amazon-ssm-agent/agent/framework/coremanager"
	agentLog "github.com/aws/amazon-ssm-agent/agent/log"
	logger "github.com/aws/amazon-ssm-agent/agent/log/ssmlog"
	messageContracts "github.com/aws/amazon-ssm-agent/agent/runcommand/contracts"
	"github.com/aws/amazon-ssm-agent/agent/s3util"
	s3utilmock "github.com/aws/amazon-ssm-agent/agent/s3util/mocks"
	"github.com/aws/amazon-ssm-agent/internal/tests/testdata"
	"github.com/aws/amazon-ssm-agent/internal/tests/testutils"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/ssmmds"
	mdssdkmock "github.com/aws/aws-sdk-go/service/ssmmds/ssmmdsiface/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

// RunCommandOutputTestSuite defines test suite for sending runcommand and verify upload to S3 bucket
type RunCommandS3OutputTestSuite struct {
	suite.Suite
	ssmAgent      agent.ISSMAgent
	mdsSdkMock    *mdssdkmock.SSMMDSAPI
	s3ServiceMock *s3utilmock.IAmazonS3Util
	log           agentLog.T
}

func (suite *RunCommandS3OutputTestSuite) SetupTest() {
	log := logger.SSMLogger(true)
	suite.log = log

	config, err := appconfig.Config(true)
	if err != nil {
		log.Debugf("appconfig could not be loaded - %v", err)
		return
	}
	context := context.Default(log, config)

	sendMdsSdkRequest := func(req *request.Request) error {
		return nil
	}
	mdsSdkMock := testutils.NewMdsSdkMock()
	mdsService := testutils.NewMdsService(mdsSdkMock, sendMdsSdkRequest)
	suite.mdsSdkMock = mdsSdkMock

	s3ServiceMock := new(s3utilmock.IAmazonS3Util)
	suite.s3ServiceMock = s3ServiceMock

	s3Creator := func(log agentLog.T, bucketName string) s3util.IAmazonS3Util {
		return suite.s3ServiceMock
	}

	// The actual runcommand core module with mocked MDS service injected
	runcommandService := testutils.NewRuncommandServiceWithWorker(context, mdsService, s3Creator)
	var modules []contracts.ICoreModule
	modules = append(modules, runcommandService)

	// Create core manager that accepts runcommand core module
	var cpm *coremanager.CoreManager
	if cpm, err = testutils.NewCoreManager(context, &modules, log); err != nil {
		log.Errorf("error occurred when starting core manager: %v", err)
		return
	}
	// Create core ssm agent
	suite.ssmAgent = &agent.SSMAgent{}
	suite.ssmAgent.SetContext(context)
	suite.ssmAgent.SetCoreManager(cpm)
}

func (suite *RunCommandS3OutputTestSuite) TearDownSuite() {
	// Close the log only after the all tests are done.
	suite.log.Close()
}

func cleanUpRunCommandS3OutputTest(suite *RunCommandS3OutputTestSuite) {
	// recover in case the agent panics
	// this should handle some kind of seg fault errors.
	if msg := recover(); msg != nil {
		suite.T().Errorf("Agent crashed with message %v!", msg)
		suite.T().Errorf("%s: %s", msg, debug.Stack())
	}
	// flush the log to get full logs after the test is done, don't close the log unless all tests are done
	suite.log.Flush()
}

//TestS3Output tests the agent by mocking MDS to send a messages to the agent and verify upload to S3 bucket
func (suite *RunCommandS3OutputTestSuite) TestS3Output() {
	// Mock MDs service so it'll return only one message and empty messages after that.
	suite.mdsSdkMock.On("GetMessagesRequest", mock.AnythingOfType("*ssmmds.GetMessagesInput")).Return(&request.Request{}, func(input *ssmmds.GetMessagesInput) *ssmmds.GetMessagesOutput {
		messageOutput, _ := testutils.GenerateMessages(testdata.EchoMDSMessageWithS3Bucket)
		return messageOutput
	}, nil).Times(1)

	suite.mdsSdkMock.On("GetMessagesRequest", mock.AnythingOfType("*ssmmds.GetMessagesInput")).Return(&request.Request{}, func(input *ssmmds.GetMessagesInput) *ssmmds.GetMessagesOutput {
		emptyMessage, _ := testutils.GenerateEmptyMessage()
		return emptyMessage
	}, nil)

	defer func() {
		cleanUpRunCommandS3OutputTest(suite)
	}()

	// a channel to block test execution untill the agent is done processing and uploading to S3
	c := make(chan int)
	suite.mdsSdkMock.On("SendReplyRequest", mock.AnythingOfType("*ssmmds.SendReplyInput")).Return(&request.Request{}, func(input *ssmmds.SendReplyInput) *ssmmds.SendReplyOutput {
		payload := input.Payload
		var sendReplyPayload messageContracts.SendReplyPayload
		json.Unmarshal([]byte(*payload), &sendReplyPayload)

		if sendReplyPayload.DocumentStatus == contracts.ResultStatusFailed || sendReplyPayload.DocumentStatus == contracts.ResultStatusTimedOut {
			suite.T().Errorf("Document execution %v", sendReplyPayload.DocumentStatus)
			c <- 1
		} else if sendReplyPayload.DocumentStatus == contracts.ResultStatusSuccess {
			suite.T().Logf("Document execution %v", sendReplyPayload.DocumentStatus)
			c <- 1
		}
		return &ssmmds.SendReplyOutput{}
	})

	suite.s3ServiceMock.On("S3Upload", mock.AnythingOfType("*log.Wrapper"), mock.AnythingOfType("string"), mock.AnythingOfType("string"), mock.AnythingOfType("string")).Return(func(log agentLog.T, bucketName string, objectKey string, filePath string) error {
		fileContent, _ := fileutil.ReadAllText(filePath)
		suite.T().Logf("Upload to S3 file content %v", fileContent)
		assert.Equal(suite.T(), bucketName, testdata.TestS3BucketName, "Agent is uploading output to the wrong bucket")
		assert.Contains(suite.T(), fileContent, testdata.EchoMDSMessageOutput, "Agent is uploading unexpected output")
		c <- 1
		return nil
	}, nil)

	// start the agent and block test until it finishes executing documents
	suite.ssmAgent.Start()
	<-c
	<-c
	// stop agent execution
	suite.ssmAgent.Stop()
}
func TestRunCommandS3OutputIntegTestSuite(t *testing.T) {
	suite.Run(t, new(RunCommandS3OutputTestSuite))
}
