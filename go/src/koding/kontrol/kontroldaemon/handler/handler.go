package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"koding/db/models"
	"koding/db/mongodb"
	"koding/db/mongodb/modelhelper"
	"koding/kontrol/kontrolhelper"
	"koding/tools/config"
	"koding/tools/logger"
	"strconv"
	"strings"
	"time"

	"github.com/streadway/amqp"
	"labix.org/v2/mgo"
	"labix.org/v2/mgo/bson"
)

const HEARTBEAT_INTERVAL = time.Second * 10
const HEARTBEAT_DELAY = time.Second * 5

type WorkerResponse struct {
	Name    string `json:"name"`
	Uuid    string `json:"uuid"`
	Command string `json:"command"`
	Log     string `json:"log"`
}

func NewWorkerResponse(name, uuid, command, log string) *WorkerResponse {
	return &WorkerResponse{
		Name:    name,
		Uuid:    uuid,
		Command: command,
		Log:     log,
	}
}

type IncomingMessage struct {
	Worker  *models.Worker
	Monitor *models.Monitor
}

var producer *kontrolhelper.Producer
var kontrolDB *mongodb.MongoDB
var log = logger.New("kontroldaemon")

const (
	WorkersCollection = "jKontrolWorkers"
	WorkersDB         = "kontrol"
)

func Startup(conf *config.Config) {
	var err error
	producer, err = kontrolhelper.CreateProducer(conf, "worker")
	if err != nil {
		log.Error(err.Error())
	}

	err = producer.Channel.ExchangeDeclare("clientExchange", "fanout", true, false, false, false, nil)
	if err != nil {
		log.Error("clientExchange exchange.declare: %s", err)
	}

	kontrolDB = mongodb.NewMongoDB(conf.MongoKontrol)
	modelhelper.KontrolWorkersInit(conf.MongoKontrol)

	go heartBeatChecker()
	go deploymentCleaner()

	log.Info("handler is initialized")
}

// WorkerMessage is handling messages coming from the workerExchange
func WorkerMessage(data []byte) {
	var msg IncomingMessage
	err := json.Unmarshal(data, &msg)
	if err != nil {
		log.Error("bad json incoming msg: %s err: %s", string(data), err)
	}

	if msg.Monitor != nil {
		err := handleMonitorData(msg.Monitor)
		if err != nil {
			log.Error(err.Error())
		}
		return
	}

	if msg.Worker != nil {
		err = handleCommand(msg.Worker.Message.Command, *msg.Worker)
		if err != nil {
			log.Error(err.Error())
		}
		return
	}

	log.Warning("incoming message is in wrong format %v", msg)
}

func handleMonitorData(data *models.Monitor) error {
	worker, err := modelhelper.GetWorker(data.Uuid)
	if err != nil {
		return fmt.Errorf("monitor data error '%s'", err)
	}

	worker.Monitor.Mem = *data.Mem
	worker.Monitor.Uptime = data.Uptime
	modelhelper.UpdateWorker(worker)
	return nil
}

// handleCommand is used to handle messages coming from workers.
func handleCommand(command string, worker models.Worker) error {
	if worker.Uuid == "" {
		fmt.Errorf("worker %s does have an empty uuid", worker.Name)
	}

	switch command {
	case "add", "addWithProxy":
		// This is a large and complex process, handle it separately.
		// "res" will be send to the worker, it contains the permission result
		res, err := handleAddCommand(worker)
		if err != nil {
			return err
		}
		go deliver(res)

		// rest is proxy related
		if command != "addWithProxy" {
			return nil
		}

		if worker.Port == 0 { // zero port is useless for proxy
			return fmt.Errorf("register to kontrol proxy not possible. port number is '0' for %s", worker.Name)
		}

		port := strconv.Itoa(worker.Port)
		key := strconv.Itoa(worker.Version)
		err = modelhelper.UpsertKey(
			"koding",    // username
			worker.Name, // servicename
			key,         // version (build number)
			worker.Hostname+":"+port, // host
			worker.Environment,       // hostdata, pass environment
			true,                     // enable keyData to be used with proxy immediately
		)
		if err != nil {
			return fmt.Errorf("register to kontrol proxy not possible: %s", err.Error())
		}
	case "ack":
		query := func(c *mgo.Collection) error {
			return c.Update(
				bson.M{"uuid": worker.Uuid},
				bson.M{"$set": bson.M{
					"timestamp": time.Now().Add(HEARTBEAT_INTERVAL),
					"status":    models.Started,
				}},
			)
		}

		err := kontrolDB.RunOnDatabase(WorkersDB, WorkersCollection, query)
		if err == mgo.ErrNotFound {
			worker.Status = models.Started
			worker.ObjectId = bson.NewObjectId()
			worker.Timestamp = time.Now().Add(HEARTBEAT_INTERVAL)
			workerLog("NOT REGISTERED, ADDING AGAIN", worker)
			return modelhelper.UpsertWorker(worker)
		}
		return err
	case "update":
		//  Update kontrold worker information with our newly created pid.
		query := func(c *mgo.Collection) error {
			return c.Update(
				bson.M{"uuid": worker.Uuid},
				bson.M{"$set": bson.M{
					"timestamp": time.Now().Add(HEARTBEAT_INTERVAL),
					"status":    models.Started,
					"pid":       worker.Pid,
					"version":   worker.Version,
				}},
			)
		}

		workerLog("UPDATE", worker)

		return kontrolDB.RunOnDatabase(WorkersDB, WorkersCollection, query)
	default:
		return fmt.Errorf(" command not recognized: %s", command)
	}

	return nil
}

func workerLog(msg string, worker models.Worker) string {
	msgLog := fmt.Sprintf("%s : %s - (hostname: %s version: %d uuid: %s pid: %d)",
		msg,
		worker.Name,
		worker.Hostname,
		worker.Version,
		worker.Uuid,
		worker.Pid,
	)

	log.Info(msgLog)
	return msgLog
}

// handleAddCommand is a router that does different things according to the
// workers' start mode. Each mode is handled via a seperate function.
func handleAddCommand(worker models.Worker) (*WorkerResponse, error) {
	switch worker.Message.Option {
	case "one", "version":
		return handleExclusiveOption(worker)
	case "many":
		return handleManyOption(worker)
	}

	return nil, errors.New("no option specified for add action. aborting add handler...")
}

// handleExclusiveOption starts workers whose are in one and version mode. These
// modes are special where the workers are allowed to be run exclusive, which
// then deny any other workers to be runned.
func handleExclusiveOption(worker models.Worker) (*WorkerResponse, error) {
	option := worker.Message.Option
	query := bson.M{}
	reason := ""

	// one means that only one single instance of the worker can work. For
	// example if we start an emailWorker with the mode "one", another
	// emailWorker don't get the permission to run.
	if option == "one" {
		query = bson.M{
			"name": worker.Name,
		}
		reason = fmt.Sprintf("workers with the same name running: ")
	}

	// version is like one, but it's allow only workers of the same name
	// and version. For example if an authWorker of version 13 starts with
	// the mode "version", than only authWorkers of version 13 can start,
	// any other authworker different than 13 (say, 10, 14, ...) don't get
	// the permission to run.
	if option == "version" {
		query = bson.M{
			"name": bson.RegEx{Pattern: "^" + normalizeName(worker.Name),
				Options: "i"},
			"version": bson.M{"$ne": worker.Version},
		}
		reason = fmt.Sprintf("workers with different name and versions running: ")
	}

	// If the query above for 'one' and 'version' doesn't match anything,
	// then add our new worker. Apply() is atomic and uses findAndModify.
	// Adding it causes no err, therefore the worker get 'start' message.
	// However if the query matches, then the 'upsert' will fail (means
	// that there is some workers that are running).
	worker.ObjectId = bson.NewObjectId()
	worker.Status = models.Started
	worker.Timestamp = time.Now().Add(HEARTBEAT_INTERVAL)
	change := mgo.Change{
		Update: worker,
		Upsert: true,
	}

	resultOfApply := new(models.Worker)

	// this is the worker that matches the query, that means a worker
	// cannot be added in mode one or version because of this worker that
	// is still alive.
	aliveWorker := new(models.Worker)

	err := kontrolDB.RunOnDatabase(WorkersDB, WorkersCollection, func(c *mgo.Collection) error {
		// worst fucking syntax ever I saw in my life that is doing
		// fucking gazillion things with one fucking method called fucking
		// apply. fuck you mgo
		_, err := c.Find(query).Apply(change, resultOfApply)

		// this is needed because of the fucking syntax above that doesn't
		// return the old document even when it MATCHES the fucking query!!!.
		// again fuck you mgo
		c.Find(query).One(aliveWorker)
		return err
	})

	if err == nil {
		startLog := workerLog("START", worker)
		return NewWorkerResponse(worker.Name, worker.Uuid, "start", startLog), nil
	}

	reasonLog := reason + fmt.Sprintf("version: %d (pid: %d) at %s", aliveWorker.Version, aliveWorker.Pid, aliveWorker.Hostname)
	return NewWorkerResponse(worker.Name, worker.Uuid, "noPermission", reasonLog), nil
}

// handleManyOption just starts the worker. That means a worker can be started as
// many times as we wished with this option.
func handleManyOption(worker models.Worker) (*WorkerResponse, error) {
	startLog := workerLog("START", worker)
	worker.ObjectId = bson.NewObjectId()
	worker.Status = models.Started
	worker.Timestamp = time.Now().Add(HEARTBEAT_INTERVAL)
	modelhelper.UpsertWorker(worker)

	return NewWorkerResponse(worker.Name, worker.Uuid, "start", startLog), nil
}

func deliver(res *WorkerResponse) {
	data, err := json.Marshal(res)
	if err != nil {
		log.Error("could not marshall worker: %s", err)
	}

	msg := amqp.Publishing{
		Headers:         amqp.Table{},
		ContentType:     "text/plain",
		ContentEncoding: "",
		Body:            data,
		DeliveryMode:    1, // 1=non-persistent, 2=persistent
		Priority:        0, // 0-9
	}

	if res.Uuid == "" {
		log.Error("can't send to worker. appId is missing")
	}

	workerOut := "output.worker." + res.Uuid
	err = producer.Channel.Publish("workerExchange", workerOut, false, false, msg)
	if err != nil {
		log.Error("error while publishing message: %s", err)
	}
}

// convert foo-1, foo-*, etc to foo
func normalizeName(name string) string {
	if counts := strings.Count(name, "-"); counts > 0 {
		return strings.Split(name, "-")[0]
	}
	return name
}

// heartBeathChecker checks if a worker is alive or not. If it's alive it's
// just continues to the next one until it finds a worker that didn't get an
// hearbeat. If that worker didn't get three heartbeats in a series we are
// removing it from the DB.
func heartBeatChecker() {
	query := func(c *mgo.Collection) error {
		worker := models.Worker{}

		iter := c.Find(nil).Iter()
		for iter.Next(&worker) {
			if time.Now().Before(worker.Timestamp) {
				continue
			}
			workerLog("NO HEARTBEAT", worker)

			if time.Now().Before(worker.Timestamp.Add(HEARTBEAT_DELAY)) {
				continue // this one is alive, pick up the next one
			}

			workerLog("DEAD", worker)
			modelhelper.DeleteWorker(worker.Uuid)
		}

		if err := iter.Close(); err != nil {
			return err
		}

		return nil
	}

	for {
		kontrolDB.RunOnDatabase(WorkersDB, WorkersCollection, query)
		time.Sleep(time.Second * 2)
	}
}

// Cleanup dead deployments at intervals. This goroutine will lookup at
// each information if a deployment has running workers. If workers for a
// certain deployment is not running anymore, then it will remove the
// deployment information .
func deploymentCleaner() {
	for {
		log.Info("cleaner started to remove unused deployments and dead workers")
		infos := modelhelper.GetClients()
		for _, info := range infos {
			var numberOfWorkers int

			version, _ := strconv.Atoi(info.BuildNumber)

			query := func(c *mgo.Collection) error {
				numberOfWorkers, _ = c.Find(bson.M{
					"version": version,
					"status":  int(models.Started)},
				).Count()

				return nil
			}

			kontrolDB.RunOnDatabase(WorkersDB, WorkersCollection, query)

			// remove deployment information only if there is no worker alive for that version
			if numberOfWorkers == 0 {
				log.Info("removing deployment info for build number %s", info.BuildNumber)
				err := modelhelper.DeleteClient(info.BuildNumber)
				if err != nil {
					log.Error(err.Error())
				}
			}
		}

		// check 12 hours later again
		time.Sleep(time.Hour * 12)
	}
}
