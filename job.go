package main

import (
	"github.com/golang/glog"
	"github.com/gorhill/cronexpr"
	p "github.com/vergult/go9p"
	"github.com/vergult/go9p/srv"

	"bytes"
	"container/ring"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	// STOPPED indicates the job is stopped
	STOPPED = "stopped"

	// STOP the ctl file command string to stop a job
	STOP = "stop"

	// STARTED indicates the job is started
	STARTED = "started"

	// START the ctl file command string to start a job
	START = "start"
)

type jobdef struct {
	name     string
	schedule string
	cmd      string
	state    string
}

type jobreader func() []byte
type jobwriter func([]byte) (int, error)

type job struct {
	srv.File
	defn    jobdef
	done    chan bool
	history *ring.Ring
}

type jobfile struct {
	srv.File
	reader jobreader
	writer jobwriter
}

// mkJob creates the subtree of files that represent a job in jobd and returns
// it to its caller.
func mkJob(root *srv.File, user p.User, def jobdef) (*job, error) {
	glog.V(4).Infof("Entering mkJob(%v, %v, %v)", root, user, def)
	defer glog.V(4).Infof("Exiting mkJob(%v, %v, %v)", root, user, def)

	glog.V(3).Infoln("Creating job directory: ", def.name)

	job := &job{defn: def, done: make(chan bool), history: ring.New(32)}

	ctl := &jobfile{
		// ctl reader returns the current state of the job.
		reader: func() []byte {
			return []byte(job.defn.state)
		},
		// ctl writer is responsible for stopping or starting the job.
		writer: func(data []byte) (int, error) {
			switch cmd := strings.ToLower(string(data)); cmd {
			case STOP:
				if job.defn.state != STOPPED {
					glog.V(3).Infof("Stopping job: %v", job.defn.name)
					job.defn.state = STOPPED
					job.done <- true
				}
				return len(data), nil
			case START:
				if job.defn.state != STARTED {
					glog.V(3).Infof("Starting job: %v", job.defn.name)
					job.defn.state = STARTED
					go job.run()
				}
				return len(data), nil
			default:
				return 0, fmt.Errorf("unknown command: %s", cmd)
			}
		}}
	if err := ctl.Add(&job.File, "ctl", user, nil, 0666, ctl); err != nil {
		glog.Errorf("Can't create %s/ctl [%v]", def.name, err)
		return nil, err
	}

	sched := &jobfile{
		// schedule reader returns the job's schedule and, if it's started, its
		// next scheduled execution time.
		reader: func() []byte {
			if job.defn.state == STARTED {
				e, _ := cronexpr.Parse(job.defn.schedule)
				return []byte(fmt.Sprintf("%s:%v", job.defn.schedule, e.Next(time.Now())))
			}
			return []byte(job.defn.schedule)
		},
		// schedule is read only.
		writer: func(data []byte) (int, error) {
			return 0, srv.Eperm
		}}
	if err := sched.Add(&job.File, "schedule", user, nil, 0444, sched); err != nil {
		glog.Errorf("Can't create %s/schedule [%v]", job.defn.name, err)
		return nil, err
	}

	cmd := &jobfile{
		// cmd reader returns the job's command.
		reader: func() []byte {
			return []byte(def.cmd)
		},
		// cmd is read only.
		writer: func(data []byte) (int, error) {
			return 0, srv.Eperm
		}}
	if err := cmd.Add(&job.File, "cmd", user, nil, 0444, cmd); err != nil {
		glog.Errorf("Can't create %s/cmd [%v]", job.defn.name, err)
		return nil, err
	}

	log := &jobfile{
		// log reader returns the job's execution history.
		reader: func() []byte {
			result := []byte{}
			job.history.Do(func(v interface{}) {
				if v != nil {
					for _, b := range bytes.NewBufferString(v.(string)).Bytes() {
						result = append(result, b)
					}
				}
			})
			return result
		},
		// log is read only.
		writer: func(data []byte) (int, error) {
			return 0, srv.Eperm
		}}
	if err := log.Add(&job.File, "log", user, nil, 0444, log); err != nil {
		glog.Errorf("Can't create %s/log [%v]", job.defn.name, err)
		return nil, err
	}

	return job, nil
}

// mkJobDefinition examines the components of a job definition it is given and
// returns a new jobdef struct containing them if they are valid.
func mkJobDefinition(name, schedule, cmd string) (*jobdef, error) {
	if ok, err := regexp.MatchString("[^[:word:]]", name); ok || err != nil {
		switch {
		case ok:
			return nil, fmt.Errorf("invalid job name: %s", name)
		default:
			return nil, err
		}
	}

	if _, err := cronexpr.Parse(schedule); err != nil {
		return nil, err
	}

	return &jobdef{name, schedule, cmd, STOPPED}, nil
}

// Read handles read operations on a jobfile using its associated reader.
func (jf jobfile) Read(fid *srv.FFid, buf []byte, offset uint64) (int, error) {
	glog.V(4).Infof("Entering jobfile.Read(%v, %v, %)", fid, buf, offset)
	defer glog.V(4).Infof("Exiting jobfile.Read(%v, %v, %v)", fid, buf, offset)

	cont := jf.reader()

	if offset > uint64(len(cont)) {
		return 0, nil
	}

	contout := cont[offset:]

	copy(buf, contout)
	return len(contout), nil
}

// Wstat doesn't do anything but support for the operation is required to make
// the OS file system calls happy.
// TODO: verify it's still necessary.
func (jf jobfile) Wstat(fid *srv.FFid, dir *p.Dir) error {
	glog.V(4).Infof("Entering jobfile.Wstat(%v, %v)", fid, dir)
	defer glog.V(4).Infof("Exiting jobfile.Wstat(%v, %v, %v)", fid, dir)

	return nil
}

// Write handles write operations on a jobfile using its associated writer.
func (jf *jobfile) Write(fid *srv.FFid, data []byte, offset uint64) (int, error) {
	glog.V(4).Infof("Entering jobfile.Write(%v, %v, %v)", fid, data, offset)
	defer glog.V(4).Infof("Exiting jobfile.Write(%v, %v, %v)", fid, data, offset)

	jf.Parent.Lock()
	defer jf.Parent.Unlock()

	return jf.writer(data)
}

// run executes the command associated with a job according to its schedule and
// records the results until it is told to stop.
func (j *job) run() {
	j.history.Value = fmt.Sprintf("%s:started\n", time.Now().String())
	j.history = j.history.Next()
	for {
		now := time.Now()
		e, err := cronexpr.Parse(j.defn.schedule)
		if err != nil {
			glog.Errorf("Can't parse %s [%s]", j.defn.schedule, err)
			return
		}

		select {
		case <-time.After(e.Next(now).Sub(now)):
			glog.V(3).Infof("running `%s`", j.defn.cmd)
			var out bytes.Buffer
			k := exec.Command("/bin/bash", "-c", j.defn.cmd)
			k.Stdout = &out
			if err := k.Run(); err != nil {
				glog.Errorf("%s failed: %v", j.defn.cmd, err)
				continue
			}
			glog.V(3).Infof("%s returned: %s", j.defn.name, out.String())
			j.history.Value = fmt.Sprintf("%s:%s", time.Now().String(), out.String())
			j.history = j.history.Next()
		case <-j.done:
			glog.V(3).Infof("completed")
			j.history.Value = fmt.Sprintf("%s:completed\n", time.Now().String())
			j.history = j.history.Next()
			return
		}
	}
}
