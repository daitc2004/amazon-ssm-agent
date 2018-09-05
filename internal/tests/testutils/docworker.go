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
	"errors"
	"github.com/aws/amazon-ssm-agent/agent/context"
	outofproc "github.com/aws/amazon-ssm-agent/agent/framework/processor/executer/outofproc"
	"github.com/aws/amazon-ssm-agent/agent/framework/processor/executer/outofproc/channel"
	"github.com/aws/amazon-ssm-agent/agent/framework/processor/executer/outofproc/messaging"
	"github.com/aws/amazon-ssm-agent/agent/framework/processor/executer/outofproc/proc"
	"github.com/aws/amazon-ssm-agent/agent/framework/runpluginutil"
	"math/rand"
	"time"
)

type FakeProcess struct {
	exitChan chan bool
	live     bool
	attached bool
}

func NewProcessCreator(context context.T, pluginManager runpluginutil.IPluginManager) outofproc.ProcessCreator {
	processCreator := func(name string, argv []string) (proc.OSProcess, error) {
		fakeProcess := NewFakeProcess()
		fakeProcess.live = true
		fakeProcess.attached = true
		docID := argv[0]
		//launch a faked worker
		go fakeProcess.fakeWorker(context, docID, pluginManager)
		return fakeProcess, nil
	}
	return processCreator
}

func NewFakeProcess() *FakeProcess {
	return &FakeProcess{
		exitChan: make(chan bool, 10),
	}
}

//replicate the same procedure as the worker main function
func (p *FakeProcess) fakeWorker(context context.T, channelName string, pluginManager runpluginutil.IPluginManager) {
	ctx := context.With("[FAKE-DOCUMENT-WORKER]").With("[" + channelName + "]")
	log := ctx.Log()
	log.Infof("document: %v process started", channelName)

	//create channel from the given handle identifier by master
	ipc, err, _ := channel.CreateFileChannel(log, channel.ModeWorker, channelName)
	if err != nil {
		log.Errorf("failed to create channel: %v", err)
		log.Close()
		return
	}

	pipeline := messaging.NewWorkerBackend(ctx, pluginManager)
	stopTimer := make(chan bool)
	if err := messaging.Messaging(log, ipc, pipeline, stopTimer); err != nil {
		log.Errorf("messaging worker encountered error: %v", err)
		//If ipc messaging broke, there's nothing worker process can do, exit immediately
		log.Close()
		return
	}
	log.Info("document worker closed")
	//ensure logs are flushed
	log.Flush()
	//process exits
	p.live = false
	p.attached = false
	//faked syscall Wait() should return now
	p.exitChan <- true
}

func (p *FakeProcess) Pid() int {
	return rand.Int()
}

func (p *FakeProcess) Kill() error {
	p.attached = false
	p.live = false
	p.exitChan <- true
	return nil
}

func (p *FakeProcess) StartTime() time.Time {
	return time.Now().UTC()
}

func (p *FakeProcess) Wait() error {
	//once the child is detached (controlled by our test engine), Wait() is illegal since the Executer is no longer the direct parent of the child
	if !p.attached {
		return errors.New("Wait() called by illegal party")
	}
	<-p.exitChan
	p.live = false
	return nil
}
