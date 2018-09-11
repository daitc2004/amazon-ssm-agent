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
	"path/filepath"
	"runtime/debug"
	"testing"

	"github.com/aws/amazon-ssm-agent/agent/agent"
	"github.com/aws/amazon-ssm-agent/agent/appconfig"
	"github.com/aws/amazon-ssm-agent/agent/context"
	"github.com/aws/amazon-ssm-agent/agent/contracts"
	"github.com/aws/amazon-ssm-agent/agent/fileutil"
	"github.com/aws/amazon-ssm-agent/agent/framework/coremanager"
	"github.com/aws/amazon-ssm-agent/agent/log"
	logger "github.com/aws/amazon-ssm-agent/agent/log/ssmlog"
	"github.com/aws/amazon-ssm-agent/agent/platform"
	messageContracts "github.com/aws/amazon-ssm-agent/agent/runcommand/contracts"
	"github.com/aws/amazon-ssm-agent/internal/tests/testdata"
	"github.com/aws/amazon-ssm-agent/internal/tests/testutils"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/ssmmds"
	mdssdkmock "github.com/aws/aws-sdk-go/service/ssmmds/ssmmdsiface/mocks"
	assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

// CrashWorkerTestSuite defines test suite for sending a command to the agent and handling the worker process crash
type CrashWorkerTestSuite struct {
	suite.Suite
	ssmAgent   agent.ISSMAgent
	mdsSdkMock *mdssdkmock.SSMMDSAPI
	log        log.T
}

func (suite *CrashWorkerTestSuite) SetupTest() {
	log := logger.SSMLogger(true)
	suite.log = log

	config, err := appconfig.Config(true)
	if err != nil {
		log.Debugf("appconfig could not be loaded - %v", err)
		return
	}
	context := context.Default(log, config)

	// Mock MDS service to remove dependency on external service
	sendMdsSdkRequest := func(req *request.Request) error {
		return nil
	}
	mdsSdkMock := testutils.NewMdsSdkMock()
	mdsService := testutils.NewMdsService(mdsSdkMock, sendMdsSdkRequest)

	suite.mdsSdkMock = mdsSdkMock

	// The actual runcommand core module with mocked MDS service injected
	runcommandService := testutils.NewRuncommandService(context, mdsService)
	var modules []contracts.ICoreModule
	modules = append(modules, runcommandService)

	// Create core manager that accepts runcommand core module
	// For this test we don't need to inject all the modules
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

func (suite *CrashWorkerTestSuite) TearDownSuite() {
	// Close the log only after the all tests are done.
	suite.log.Close()
}

func cleanUpCrashWorkerTest(suite *CrashWorkerTestSuite) {
	// recover in case the agent panics
	// this should handle some kind of seg fault errors.
	if msg := recover(); msg != nil {
		suite.T().Errorf("Agent crashed with message %v!", msg)
		suite.T().Errorf("%s: %s", msg, debug.Stack())
	}
	// flush the log to get full logs after the test is done, don't close the log unless all tests are done
	suite.log.Flush()
}

//TestDocumentWorkerCrash tests the agent recovers is the document worker crashes and sends valid results
func (suite *CrashWorkerTestSuite) TestDocumentWorkerCrash() {
	suite.mdsSdkMock.On("GetMessagesRequest", mock.AnythingOfType("*ssmmds.GetMessagesInput")).Return(&request.Request{}, func(input *ssmmds.GetMessagesInput) *ssmmds.GetMessagesOutput {
		messageOutput, _ := testutils.GenerateMessages(testdata.CrashWorkerMDSMessage)
		return messageOutput
	}, nil)

	suite.mdsSdkMock.On("GetMessagesRequest", mock.AnythingOfType("*ssmmds.GetMessagesInput")).Return(&request.Request{}, func(input *ssmmds.GetMessagesInput) *ssmmds.GetMessagesOutput {
		emptyMessage, _ := testutils.GenerateEmptyMessage()
		return emptyMessage
	}, nil)

	defer func() {
		cleanUpCrashWorkerTest(suite)
	}()

	// a channel to block test execution untill the agent is done processing the required number of messages
	c := make(chan int)
	suite.mdsSdkMock.On("SendReplyRequest", mock.AnythingOfType("*ssmmds.SendReplyInput")).Return(&request.Request{}, func(input *ssmmds.SendReplyInput) *ssmmds.SendReplyOutput {
		payload := input.Payload
		var sendReplyPayload messageContracts.SendReplyPayload
		json.Unmarshal([]byte(*payload), &sendReplyPayload)

		if sendReplyPayload.DocumentStatus == contracts.ResultStatusFailed {
			suite.T().Logf("Document execution %v", sendReplyPayload.DocumentStatus)
			foundPlugin := false
			for _, pluginStatus := range sendReplyPayload.RuntimeStatus {
				if pluginStatus.Status == contracts.ResultStatusFailed {
					foundPlugin = true
					assert.Contains(suite.T(), pluginStatus.Output, testdata.CrashWorkerErrorMessgae, "plugin output doesn't contain the expected error message")
				}
			}
			if !foundPlugin {
				suite.T().Error("Couldn't find plugin with result status failed")
			}
			//Verify that the agent cleans up document state directories after worker crashes
			folders := []string{
				appconfig.DefaultLocationOfPending,
				appconfig.DefaultLocationOfCurrent,
				appconfig.DefaultLocationOfCompleted,
				appconfig.DefaultLocationOfCorrupt}
			instanceId, _ := platform.InstanceID()
			for _, folder := range folders {
				directoryName := filepath.Join(appconfig.DefaultDataStorePath,
					instanceId,
					appconfig.DefaultDocumentRootDirName,
					appconfig.DefaultLocationOfState,
					folder)
				isDirEmpty, _ := fileutil.IsDirEmpty(directoryName)
				suite.T().Logf("Checking directory %s", directoryName)
				assert.True(suite.T(), isDirEmpty, "Directory is not empty")

			}
			c <- 1
		} else if sendReplyPayload.DocumentStatus == contracts.ResultStatusSuccess {
			suite.T().Errorf("Document execution %v but it was supposed to fail", sendReplyPayload.DocumentStatus)
			c <- 1
		}
		return &ssmmds.SendReplyOutput{}
	})

	// start the agent and block test until it finishes executing documents
	suite.ssmAgent.Start()
	<-c

	// stop agent execution
	suite.ssmAgent.Stop()
}

//TestDocumentWorkerCrashMultiStepDocument tests the agent recovers if the document worker crashes and sends valid results, and executes other document steps successfully
/* func (suite *CrashWorkerTestSuite) TestDocumentWorkerCrashMultiStepDocument() {
	suite.mdsSdkMock.On("GetMessagesRequest", mock.AnythingOfType("*ssmmds.GetMessagesInput")).Return(&request.Request{}, func(input *ssmmds.GetMessagesInput) *ssmmds.GetMessagesOutput {
		messageOutput, _ := testutils.GenerateMessages(testdata.CrashWorkerMultiStepMDSMessage)
		return messageOutput
	}, nil)

	suite.mdsSdkMock.On("GetMessagesRequest", mock.AnythingOfType("*ssmmds.GetMessagesInput")).Return(&request.Request{}, func(input *ssmmds.GetMessagesInput) *ssmmds.GetMessagesOutput {
		emptyMessage, _ := testutils.GenerateEmptyMessage()
		return emptyMessage
	}, nil)

	defer func() {
		cleanUpCrashWorkerTest(suite)
	}()

	// a channel to block test execution untill the agent is done processing the required number of messages
	c := make(chan int)
	suite.mdsSdkMock.On("SendReplyRequest", mock.AnythingOfType("*ssmmds.SendReplyInput")).Return(&request.Request{}, func(input *ssmmds.SendReplyInput) *ssmmds.SendReplyOutput {
		payload := input.Payload
		var sendReplyPayload messageContracts.SendReplyPayload
		json.Unmarshal([]byte(*payload), &sendReplyPayload)

		if sendReplyPayload.DocumentStatus == contracts.ResultStatusFailed {
			suite.T().Logf("Document execution %v", sendReplyPayload.DocumentStatus)
			foundPlugin := false
			for _, pluginStatus := range sendReplyPayload.RuntimeStatus {
				if pluginStatus.Status == contracts.ResultStatusFailed {
					foundPlugin = true
					assert.Contains(suite.T(), pluginStatus.Output, testdata.CrashWorkerErrorMessgae, "plugin output doesn't contain the expected error message")
				}
			}
			if !foundPlugin {
				suite.T().Error("Couldn't find plugin with result status failed")
			}
			//Verify that the agent cleans up document state directories after worker crashes
			folders := []string{
				appconfig.DefaultLocationOfPending,
				appconfig.DefaultLocationOfCurrent,
				appconfig.DefaultLocationOfCompleted,
				appconfig.DefaultLocationOfCorrupt}

			instanceId, _ := platform.InstanceID()
			for _, folder := range folders {
				directoryName := filepath.Join(appconfig.DefaultDataStorePath,
					instanceId,
					appconfig.DefaultDocumentRootDirName,
					appconfig.DefaultLocationOfState,
					folder)
				isDirEmpty, _ := fileutil.IsDirEmpty(directoryName)
				suite.T().Logf("Checking directory %s", directoryName)
				assert.True(suite.T(), isDirEmpty, "Directory is not empty")
			}
			c <- 1
		} else if sendReplyPayload.DocumentStatus == contracts.ResultStatusSuccess {
			suite.T().Errorf("Document execution %v but it was supposed to fail", sendReplyPayload.DocumentStatus)
			c <- 1
		}
		return &ssmmds.SendReplyOutput{}
	})

	// start the agent and block test until it finishes executing documents
	suite.ssmAgent.Start()
	<-c

	// stop agent execution
	suite.ssmAgent.Stop()
} */

func TestCrashWorkerTestSuite(t *testing.T) {
	suite.Run(t, new(CrashWorkerTestSuite))
}
