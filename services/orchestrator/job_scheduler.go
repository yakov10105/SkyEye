package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"time"

	"github.com/go-co-op/gocron"
	"github.com/go-redsync/redsync/v4"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"

	v1 "SkyEye/api/v1"
	"SkyEye/internal/cache"
	"SkyEye/internal/messaging"
)

// JobScheduler manages the scheduling of jobs.
type JobScheduler struct {
	instanceID  string
	cfg         *Config
	cacheClient *cache.Client
	dbClient    *gorm.DB
	mqClient    *messaging.Connection
	cron        *gocron.Scheduler
	logger      *slog.Logger
}

// NewJobScheduler creates a new JobScheduler.
func NewJobScheduler(cfg *Config, cacheClient *cache.Client, dbClient *gorm.DB, mqClient *messaging.Connection, logger *slog.Logger) *JobScheduler {
	instanceID := uuid.New().String()
	cron := gocron.NewScheduler(time.UTC)
	cron.SetMaxConcurrentJobs(1, gocron.RescheduleMode)

	return &JobScheduler{
		instanceID:  instanceID,
		cfg:         cfg,
		cacheClient: cacheClient,
		dbClient:    dbClient,
		mqClient:    mqClient,
		cron:        cron,
		logger:      logger,
	}
}

// Start begins the job scheduling loop.
func (s *JobScheduler) Start() error {
	s.logger.Info("starting orchestrator scheduler", "instanceID", s.instanceID, "schedule", s.cfg.Schedule)
	_, err := s.cron.Every(s.cfg.Schedule).Do(func() {
		s.runJob(context.Background())
	})
	if err != nil {
		s.logger.Error("failed to schedule job", "error", err)
		return fmt.Errorf("failed to schedule job: %w", err)
	}
	s.cron.StartAsync()
	return nil
}

// Stop gracefully stops the scheduler.
func (s *JobScheduler) Stop() {
	s.logger.Info("stopping orchestrator scheduler")
	s.cron.Stop()
}

// runJob is the main function executed by the scheduler. It attempts to gain
// leadership and, if successful, dispatches jobs.
func (s *JobScheduler) runJob(ctx context.Context) {
	s.logger.Debug("running scheduled job", "instanceID", s.instanceID)

	// Try to acquire leadership lock
	mutex, err := s.cacheClient.Lock(ctx, leaderKey, lockTTL)
	if err != nil {
		if errors.Is(err, redsync.ErrFailed) {
			s.logger.Debug("could not acquire lock, another instance is leader", "instanceID", s.instanceID)
		} else {
			s.logger.Error("failed to acquire lock", "error", err)
		}
		return
	}
	defer s.cacheClient.Unlock(ctx, mutex)

	s.logger.Info("acquired leadership, dispatching jobs", "instanceID", s.instanceID)
	s.dispatchJobs(ctx)
}

// dispatchJobs fetches due jobs from the database and publishes them to the message queue.
func (s *JobScheduler) dispatchJobs(ctx context.Context) {
	// This struct represents the job model stored in the database.
	type Job struct {
		ID  uint   `gorm:"primaryKey"`
		URL string `gorm:"column:url"`
		// other fields like status, etc.
	}

	s.logger.Info("fetching jobs from database...", "batch_size", jobsBatchSize)

	var jobs []Job
	result := s.dbClient.WithContext(ctx).Clauses(dbresolver.Read).Where("status = ?", "pending").Limit(jobsBatchSize).Find(&jobs)
	if result.Error != nil {
		s.logger.Error("failed to fetch jobs from database", "error", result.Error)
		return
	}

	if len(jobs) == 0 {
		s.logger.Info("no pending jobs found")
		return
	}

	s.logger.Info("found jobs to dispatch", "count", len(jobs))
	for _, job := range jobs {
		scrapeJob := &v1.ScrapeJob{
			JobId:         fmt.Sprintf("%d", job.ID),
			Url:           job.URL,
			MaxRetries:    3, // This could be a field in the DB job model
			CorrelationId: uuid.New().String(),
		}

		payload, err := proto.Marshal(scrapeJob)
		if err != nil {
			s.logger.Error("failed to marshal scrape job", "jobID", job.ID, "error", err)
			continue
		}

		err = s.mqClient.Publish(ctx, "", s.cfg.JobQueueName, payload) // Default exchange
		if err != nil {
			s.logger.Error("failed to publish job to message queue", "jobID", job.ID, "error", err)
			continue
		}
		s.logger.Debug("dispatched job", "jobID", job.ID, "correlationID", scrapeJob.CorrelationId)
	}
	s.logger.Info("finished dispatching jobs", "count", len(jobs))
}
