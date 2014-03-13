package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/wkharold/jobd/deps/code.google.com/p/go9p/p"
	"github.com/wkharold/jobd/deps/code.google.com/p/go9p/p/srv"
	"github.com/wkharold/jobd/deps/github.com/golang/glog"
)

type clonefile struct {
	srv.File
}

func mkCloneFile(dir *srv.File, user p.User) error {
	glog.V(4).Infoln("Entering mkCloneFile(%v, %v)", dir, user)
	defer glog.V(4).Infoln("Exiting mkCloneFile(%v, %v)", dir, user)

	glog.V(3).Infoln("Create the clone file")

	k := new(clonefile)
	if err := k.Add(dir, "clone", user, nil, 0666, k); err != nil {
		glog.Errorln("Can't create clone file: ", err)
		return err
	}

	return nil
}

func (k *clonefile) Write(fid *srv.FFid, data []byte, offset uint64) (int, error) {
	glog.V(4).Infof("Entering clonefile.Write(%v, %v, %v)", fid, data, offset)
	defer glog.V(4).Infof("Exiting clonefile.Write(%v, %v, %v)", fid, data, offset)

	k.Lock()
	defer k.Unlock()

	glog.V(3).Infof("Create a new job from: %s", string(data))

	jdparts := strings.Split(string(data), ":")
	if len(jdparts) != 3 {
		return 0, fmt.Errorf("invalid job definition: %s", string(data))
	}

	jd, err := mkJobDefinition(jdparts[0], jdparts[1], jdparts[2])
	if err != nil {
		return 0, err
	}

	if err := jobsroot.addJob(*jd); err != nil {
		return len(data), err
	}

	switch db, err := os.OpenFile(jobsdb, os.O_WRONLY|os.O_APPEND, 0755); {
	case err != nil:
		return len(data), err
	default:
		fmt.Fprintf(db, "%s\n", string(data))
		db.Close()
	}

	return len(data), nil
}

func (k *clonefile) Wstat(fid *srv.FFid, dir *p.Dir) error {
	glog.V(4).Infof("Entering clonefile.Wstat(%v, %v)", fid, dir)
	defer glog.V(4).Infof("Exiting clonefile.Wstat(%v, %v)", fid, dir)

	return nil
}