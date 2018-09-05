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

// Package testutils represents the common logic needed for agent tests
package testutils

import (
	"github.com/aws/amazon-ssm-agent/agent/context"
	"github.com/aws/amazon-ssm-agent/agent/contracts"
	"github.com/aws/amazon-ssm-agent/agent/framework/processor"
	"github.com/aws/amazon-ssm-agent/agent/framework/processor/executer"
	"github.com/aws/amazon-ssm-agent/agent/framework/processor/executer/basicexecuter"
	"github.com/aws/amazon-ssm-agent/agent/framework/processor/executer/iohandler"
	"github.com/aws/amazon-ssm-agent/agent/framework/processor/executer/outofproc"
	"github.com/aws/amazon-ssm-agent/agent/framework/runpluginutil"
	"github.com/aws/amazon-ssm-agent/agent/log"
	"github.com/aws/amazon-ssm-agent/agent/runcommand"
	mds "github.com/aws/amazon-ssm-agent/agent/runcommand/mds"
	"github.com/aws/amazon-ssm-agent/agent/s3util"
)

//NewRuncommandService creates actual runcommand coremodule with mock mds service injected
func NewRuncommandService(context context.T, mdsService mds.Service) *runcommand.RunCommandService {
	mdsName := "MessagingDeliveryService"
	cancelWorkersLimit := 3
	messageContext := context.With("[" + mdsName + "]")
	config := context.AppConfig()

	return runcommand.NewService(messageContext, mdsName, mdsService, config.Mds.CommandWorkersLimit, cancelWorkersLimit, false, []contracts.DocumentType{contracts.SendCommand, contracts.CancelCommand})
}

//NewRuncommandService creates actual runcommand coremodule with mock mds service injected
func NewRuncommandServiceWithWorker(c context.T, mdsService mds.Service, s3UtilCreator s3util.S3UtilCreator) *runcommand.RunCommandService {
	mdsName := "MessagingDeliveryService"
	cancelWorkersLimit := 3
	messageContext := c.With("[" + mdsName + "]")
	config := c.AppConfig()
	ioHandlerCreator := func(l log.T, ioConfig contracts.IOConfiguration) *iohandler.DefaultIOHandler {
		return iohandler.NewDefaultIOHandlerWithS3Creator(l, ioConfig, s3UtilCreator)
	}
	executerCreator := func(ctx context.T) executer.Executer {
		pluginManager := runpluginutil.NewPluginManagerWithIoHandlerCreator(ioHandlerCreator)
		processCreator := NewProcessCreator(ctx, pluginManager)
		basicExecuter := basicexecuter.NewBasicExecuter(ctx)
		return outofproc.NewOutOfProcExecuterWithProcessCreator(ctx, processCreator, basicExecuter)
	}
	processor := processor.NewEngineProcessorWithExecuter(messageContext, config.Mds.CommandWorkersLimit, cancelWorkersLimit, []contracts.DocumentType{contracts.SendCommand, contracts.CancelCommand}, executerCreator)
	return runcommand.NewServiceWithProcessor(messageContext, mdsName, mdsService, false, processor)
}
