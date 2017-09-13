// Copyright (c) 2017
// Author: Jeff Weisberg <jaw @ tcp4me.com>
// Created: 2017-Sep-11 23:28 (EDT)
// Function:

package prog

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	"argus/configure"
	"argus/diag"
	"argus/service"
)

type Conf struct {
	Command    string
	IgnoreExit bool
}

type Prog struct {
	S  *service.Service
	Cf Conf
}

var dl = diag.Logger("prog")

func init() {
	// register with service factory
	service.Register("Prog", New)
}

func New(conf *configure.CF, s *service.Service) service.Monitor {
	p := &Prog{S: s}
	return p
}

func (p *Prog) Config(conf *configure.CF, s *service.Service) error {

	conf.InitFromConfig(&p.Cf, "prog", "")
	dl.Debug("prog config")

	// validate
	if p.Cf.Command == "" {
		return errors.New("command not specified")
	}

	// set names + labels
	uname := "Prog_" + p.Cf.Command

	s.SetNames(uname, "Prog", p.Cf.Command)

	return nil
}

func (p *Prog) Init() error {
	return nil
}

func (p *Prog) Recycle() {
}
func (p *Prog) Abort() {
}
func (p *Prog) DoneConfig() {
}

func (p *Prog) Start(s *service.Service) {

	s.Debug("prog start")
	defer s.Done()

	res, fail := p.RunProg()
	if fail {
		return
	}
	s.CheckValue(res, "data")
}

func (p *Prog) RunProg() (string, bool) {

	timeout := time.Duration(p.S.Cf.Timeout) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	p.S.Debug("running '%s'", p.Cf.Command)
	cmd := exec.CommandContext(ctx, "sh", "-c", p.Cf.Command)

	// if we are running as root, attempt to switch to a nonpriveleged uid
	if os.Geteuid() == 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{
				Uid: 65535,
				Gid: 65535,
			},
		}
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		dl.Debug("command failed: %v", err)
		if !p.Cf.IgnoreExit {
			p.S.Fail("command failed")
			return "", true
		}
	}
	if len(out) > 0 {
		dl.Debug("command output: %s", out)
	}

	return string(out), false
}