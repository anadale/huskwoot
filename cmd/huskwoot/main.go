package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/adrg/xdg"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/spf13/cobra"

	"github.com/anadale/huskwoot/internal/agent"
	"github.com/anadale/huskwoot/internal/ai"
	"github.com/anadale/huskwoot/internal/api"
	"github.com/anadale/huskwoot/internal/channel"
	"github.com/anadale/huskwoot/internal/config"
	"github.com/anadale/huskwoot/internal/dateparse"
	devicesstore "github.com/anadale/huskwoot/internal/devices"
	"github.com/anadale/huskwoot/internal/events"
	huskwootI18n "github.com/anadale/huskwoot/internal/i18n"
	"github.com/anadale/huskwoot/internal/model"
	"github.com/anadale/huskwoot/internal/pairing"
	"github.com/anadale/huskwoot/internal/pipeline"
	"github.com/anadale/huskwoot/internal/push"
	"github.com/anadale/huskwoot/internal/reminder"
	"github.com/anadale/huskwoot/internal/sink"
	"github.com/anadale/huskwoot/internal/storage"
	"github.com/anadale/huskwoot/internal/usecase"
)

var version = "dev"

// rootFlags holds the shared flags for the huskwoot command tree.
type rootFlags struct {
	configDir string
	logLevel  string
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	flags := &rootFlags{}

	cmd := &cobra.Command{
		Use:     "huskwoot",
		Short:   "Personal promise tracker",
		Version: version,
		RunE: func(cmd *cobra.Command, args []string) error {
			return serveRunE(cmd.Context(), flags)
		},
	}

	defaultConfig := os.Getenv("HUSKWOOT_CONFIG_DIR")
	if defaultConfig == "" {
		defaultConfig = filepath.Join(xdg.ConfigHome, "huskwoot")
	}

	cmd.PersistentFlags().StringVar(&flags.configDir, "config-dir", defaultConfig, "path to the configuration directory")
	cmd.PersistentFlags().StringVar(&flags.logLevel, "log-level", "info", "log level (debug, info, warn, error)")

	cmd.AddCommand(newServeCommand(flags))
	cmd.AddCommand(newDevicesCommand(flags))
	return cmd
}

// newServeCommand is an explicit serve subcommand that mirrors the root behaviour
// for readable CLI scripts and future subcommands such as devices.
func newServeCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start daemon: Telegram/IMAP + HTTP API (if enabled)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return serveRunE(cmd.Context(), flags)
		},
	}
}

func serveRunE(parentCtx context.Context, flags *rootFlags) error {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	return run(parentCtx, flags.configDir, flags.logLevel)
}

func run(parentCtx context.Context, configDir string, logLevelStr string) error {
	cfg, err := config.Load(configDir)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	logger := setupLogger(logLevelStr)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(parentCtx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("Huskwoot starting")

	db, err := storage.OpenDB(filepath.Join(configDir, "huskwoot.db"))
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()

	promptOverrides, err := loadPromptOverrides(configDir)
	if err != nil {
		return fmt.Errorf("loading prompt templates: %w", err)
	}

	loc := resolveTimezone(cfg.DateTime.Timezone)
	nowFn := func() time.Time { return time.Now().In(loc) }

	weekdays := parseWeekdays(cfg.DateTime.Weekdays)
	dateTimeCfg := dateparse.Config{
		TimeOfDay: dateparse.TimeOfDay{
			Morning:   cfg.DateTime.TimeOfDay.Morning,
			Lunch:     cfg.DateTime.TimeOfDay.Lunch,
			Afternoon: cfg.DateTime.TimeOfDay.Afternoon,
			Evening:   cfg.DateTime.TimeOfDay.Evening,
		},
		Weekdays: weekdays,
	}
	dateLang := dateparse.NewDateLanguage(cfg.User.Language)
	dp := dateparse.New(dateTimeCfg, dateLang)

	hist := storage.NewSQLiteHistory(db, storage.SQLiteHistoryOptions{
		MaxMessages: cfg.History.MaxMessages,
		TTL:         cfg.History.TTL,
	})
	stateStore := storage.NewSQLiteStateStore(db)
	metaStore := storage.NewSQLiteMetaStore(db)
	sqliteTaskStore, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		return fmt.Errorf("initializing task store: %w", err)
	}
	taskStore := storage.NewCachedTaskStore(sqliteTaskStore)

	aiComps, err := buildAIComponents(cfg, promptOverrides, resolveUserName(cfg), taskStore, nowFn, dateTimeCfg, cfg.User.Language)
	if err != nil {
		return err
	}

	guardStore := storage.NewSQLiteGuardStore(db)
	telegramBot, tgChannels, notifiers, err := buildTelegramComponents(cfg, stateStore, guardStore, hist)
	if err != nil {
		return err
	}
	dmNotifier, err := buildDMNotifier(telegramBot, cfg.User.TelegramUserID, cfg.User.Language)
	if err != nil {
		return err
	}
	if dmNotifier != nil {
		notifiers = append(notifiers, dmNotifier)
	}

	channels := tgChannels
	if len(cfg.Channels.IMAP) > 0 {
		channels = append(channels, buildIMAPChannel(cfg, stateStore))
	}

	deviceStore := devicesstore.NewSQLiteDeviceStore(db)
	eventStore := events.NewSQLiteEventStore(db)
	pushQueue := push.NewSQLitePushQueue(db)
	broker := events.NewBroker(events.BrokerConfig{})

	var relayClient push.RelayClient
	if cfg.Push.Enabled() {
		relayClient = push.NewHTTPRelayClient(push.HTTPRelayClientConfig{
			BaseURL:    cfg.Push.RelayURL,
			InstanceID: cfg.Push.InstanceID,
			Secret:     []byte(cfg.Push.InstanceSecret),
			Timeout:    cfg.Push.Timeout,
			Clock:      nowFn,
			Logger:     logger,
		})
	} else {
		relayClient = push.NilRelayClient{}
	}
	pushTemplates := push.NewTemplates(cfg.DateTime.Timezone)

	projectSvc := usecase.NewProjectService(usecase.ProjectServiceDeps{
		DB:      db,
		Tasks:   taskStore,
		Meta:    metaStore,
		Events:  eventStore,
		Devices: deviceStore,
		Queue:   pushQueue,
		Broker:  broker,
	})
	taskSvc := usecase.NewTaskService(usecase.TaskServiceDeps{
		DB:      db,
		Tasks:   taskStore,
		Events:  eventStore,
		Devices: deviceStore,
		Queue:   pushQueue,
		Broker:  broker,
	})

	agentBundle, err := huskwootI18n.NewBundle(cfg.User.Language)
	if err != nil {
		return fmt.Errorf("i18n bundle: %w", err)
	}
	agentLoc := huskwootI18n.NewLocalizer(agentBundle, cfg.User.Language)

	agentTools := []agent.Tool{
		agent.NewCreateProjectTool(projectSvc, agentLoc),
		agent.NewListProjectsTool(projectSvc, agentLoc),
		agent.NewSetProjectTool(projectSvc, agentLoc),
		agent.NewCreateTaskTool(taskSvc, projectSvc, dp, agentLoc),
		agent.NewListTasksTool(taskSvc, agentLoc),
		agent.NewCompleteTaskTool(taskSvc, agentLoc),
		agent.NewMoveTaskTool(taskSvc, projectSvc, agentLoc),
	}
	ag, err := agent.New(aiComps.smartClient, agentTools, agent.Config{
		SystemPrompt: promptOverrides.agentSystem,
		Language:     cfg.User.Language,
		Now:          nowFn,
		ListProjects: projectSvc.ListProjects,
	}, logger)
	if err != nil {
		return fmt.Errorf("initializing agent: %w", err)
	}

	chatSvc := usecase.NewChatService(usecase.ChatServiceDeps{
		Agent:  ag,
		DB:     db,
		Events: eventStore,
		Broker: broker,
	})

	pipe := pipeline.New(pipeline.Config{
		OwnerIDs:    collectOwnerIDs(cfg),
		Aliases:     cfg.User.Aliases,
		Tasks:       taskSvc,
		Projects:    projectSvc,
		Chat:        chatSvc,
		Classifiers: buildClassifiers(aiComps),
		Extractors:  buildExtractors(aiComps),
		Notifiers:   notifiers,
		Logger:      logger,
	})

	msgCh := make(chan model.Message, 100)
	watchHandler := func(ctx context.Context, msg model.Message) error {
		select {
		case msgCh <- msg:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	ownerBot := telegramBot
	if ownerBot == nil && cfg.API.Enabled {
		logger.Warn("pairing: channels.telegram not configured, magic-link will only appear in logs")
	}

	pairingStore := pairing.NewSQLiteStore(db)
	broadcaster := pairing.NewBroadcaster()
	sender := pairing.NewTelegramSender(ownerBot, logger)
	pairingSvc := usecase.NewPairingService(usecase.PairingDeps{
		DB:              db,
		PairingStore:    pairingStore,
		DeviceStore:     deviceStore,
		Sender:          sender,
		Broadcaster:     broadcaster,
		Relay:           relayClient,
		OwnerChatID:     cfg.User.TelegramUserID,
		LinkTTL:         cfg.API.PairingLinkTTL,
		LongPoll:        cfg.API.PairingStatusLongPoll,
		ExternalBaseURL: cfg.API.ExternalBaseURL,
		Now:             nowFn,
		Logger:          logger,
	})

	retentionRunner := events.NewRunner(events.RunnerConfig{
		Events:          eventStore,
		Queue:           pushQueue,
		Pairing:         pairingStore,
		Devices:         deviceStore,
		Revoker:         relayClient,
		EventRetention:  cfg.API.EventsRetention,
		DeviceInactive:  cfg.Devices.InactiveThreshold,
		DeviceRetention: cfg.Devices.RetentionPeriod,
		Logger:          logger,
	})
	go retentionRunner.Run(ctx)
	logger.Info("retention runner active",
		"events_retention", cfg.API.EventsRetention,
		"device_inactive", cfg.Devices.InactiveThreshold,
		"device_retention", cfg.Devices.RetentionPeriod,
	)

	if cfg.Push.Enabled() {
		dispatcher := push.NewDispatcher(push.DispatcherDeps{
			Queue:       pushQueue,
			Events:      eventStore,
			Devices:     deviceStore,
			Relay:       relayClient,
			Templates:   pushTemplates,
			Clock:       nowFn,
			Logger:      logger,
			Interval:    cfg.Push.DispatcherInterval,
			BatchSize:   cfg.Push.BatchSize,
			MaxAttempts: cfg.Push.RetryMaxAttempts,
		})
		go func() {
			if err := dispatcher.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("push: dispatcher exited with error", "error", err)
			}
		}()
		logger.Info("push: dispatcher started", "relay_url", cfg.Push.RelayURL, "instance_id", cfg.Push.InstanceID)
	} else {
		logger.Warn("push: [push] section not configured, dispatcher disabled")
	}

	if cfg.API.Enabled {
		apiSrv := api.New(api.Config{
			ListenAddr:       cfg.API.ListenAddr,
			RequestTimeout:   cfg.API.RequestTimeout,
			ChatTimeout:      cfg.API.ChatTimeout,
			Logger:           logger,
			DB:               db,
			Devices:          deviceStore,
			Projects:         projectSvc,
			Tasks:            taskSvc,
			Chat:             chatSvc,
			History:          hist,
			Events:           eventStore,
			Broker:           broker,
			PairingService:   pairingSvc,
			PairingRateLimit: cfg.API.RateLimitPairPerHour,
			PairingSecure:    strings.HasPrefix(cfg.API.ExternalBaseURL, "https://"),
			Relay:            relayClient,
			Owner: api.OwnerInfo{
				UserName:       cfg.User.UserName,
				TelegramUserID: cfg.User.TelegramUserID,
			},
		})
		go func() {
			if err := apiSrv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("HTTP API server exited with error", "error", err)
			}
		}()
		logger.Info("HTTP API active", "addr", cfg.API.ListenAddr)
	} else {
		logger.Debug("HTTP API disabled")
	}

	reminderSched, err := buildReminderScheduler(cfg, telegramBot, taskStore, weekdays, loc, nowFn, cfg.User.Language, logger)
	if err != nil {
		return err
	}
	if reminderSched != nil {
		go func() {
			if err := reminderSched.Run(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				logger.Error("summary scheduler", "error", err)
			}
		}()
		sched := cfg.Reminders.Schedule
		slotNames := []string{"morning"}
		if sched.Afternoon != "" {
			slotNames = append(slotNames, "afternoon")
		}
		if sched.Evening != "" {
			slotNames = append(slotNames, "evening")
		}
		logger.Info("reminders active", "slots", slotNames, "send_when_empty", cfg.Reminders.SendWhenEmpty)
	} else {
		logger.Debug("reminders disabled")
	}

	for _, ch := range channels {
		go func(ch model.Channel) {
			if err := ch.Watch(ctx, watchHandler); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				logger.Error("channel exited with error", "error", err)
			}
		}(ch)
	}

	for _, n := range notifiers {
		logger.Info("notifier active", "name", n.Name())
	}

	logger.Info("Huskwoot running, waiting for messages")

	for {
		select {
		case <-ctx.Done():
			logger.Info("Huskwoot shutting down")
			return nil
		case msg := <-msgCh:
			if err := pipe.Process(ctx, msg); err != nil {
				logger.Error("message processing error", "error", err)
			}
		}
	}
}

func setupLogger(logLevelStr string) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(logLevelStr)); err != nil {
		level = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func resolveUserName(cfg *config.Config) string {
	return cfg.User.UserName
}

func collectOwnerIDs(cfg *config.Config) []string {
	return []string{fmt.Sprintf("%d", cfg.User.TelegramUserID)}
}

func resolveTimezone(tzStr string) *time.Location {
	if tzStr == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(tzStr)
	if err != nil {
		slog.Warn("invalid timezone in config, falling back to local", "timezone", tzStr, "error", err)
		return time.Local
	}
	return loc
}

func parseWeekdays(weekdayStrs []string) []time.Weekday {
	if len(weekdayStrs) == 0 {
		return []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}
	}

	weekdayMap := map[string]time.Weekday{
		"mon": time.Monday,
		"tue": time.Tuesday,
		"wed": time.Wednesday,
		"thu": time.Thursday,
		"fri": time.Friday,
		"sat": time.Saturday,
		"sun": time.Sunday,
	}

	var weekdays []time.Weekday
	for _, s := range weekdayStrs {
		s = strings.ToLower(strings.TrimSpace(s))
		if wd, ok := weekdayMap[s]; ok {
			weekdays = append(weekdays, wd)
		} else {
			slog.Warn("unknown weekday in config, skipping", "weekday", s)
		}
	}

	if len(weekdays) == 0 {
		return []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}
	}

	return weekdays
}

type aiComponents struct {
	simpleClassifier model.Classifier
	groupClassifier  model.Classifier
	extractor        model.Extractor
	smartClient      *ai.Client
}

func buildAIComponents(cfg *config.Config, overrides promptOverrides, userName string, taskStore model.TaskStore, nowFn func() time.Time, dateTimeCfg dateparse.Config, lang string) (aiComponents, error) {
	fastClient := ai.NewClient(ai.ClientConfig{
		BaseURL:             cfg.AI.Fast.BaseURL,
		APIKey:              cfg.AI.Fast.APIKey,
		Model:               cfg.AI.Fast.Model,
		MaxCompletionTokens: cfg.AI.Fast.MaxCompletionTokens,
	})
	smartClient := ai.NewClient(ai.ClientConfig{
		BaseURL:             cfg.AI.Smart.BaseURL,
		APIKey:              cfg.AI.Smart.APIKey,
		Model:               cfg.AI.Smart.Model,
		MaxCompletionTokens: cfg.AI.Smart.MaxCompletionTokens,
	})

	classifierCfg := ai.ClassifierConfig{
		UserName: userName,
		Aliases:  cfg.User.Aliases,
		Language: lang,
	}

	simpleClassifier, err := ai.NewSimpleClassifier(fastClient, ai.ClassifierConfig{
		UserName:       classifierCfg.UserName,
		Aliases:        classifierCfg.Aliases,
		Language:       classifierCfg.Language,
		SystemTemplate: overrides.simpleClassifierSystem,
	})
	if err != nil {
		return aiComponents{}, fmt.Errorf("initializing classifier (simple): %w", err)
	}

	groupClassifier, err := ai.NewGroupClassifier(fastClient, ai.ClassifierConfig{
		UserName:       classifierCfg.UserName,
		Aliases:        classifierCfg.Aliases,
		Language:       classifierCfg.Language,
		SystemTemplate: overrides.groupClassifierSystem,
	})
	if err != nil {
		return aiComponents{}, fmt.Errorf("initializing group classifier: %w", err)
	}

	projectsFn := func(ctx context.Context) ([]string, error) {
		projects, err := taskStore.ListProjects(ctx)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(projects))
		for _, p := range projects {
			names = append(names, p.Name)
		}
		return names, nil
	}

	extractor, err := ai.NewTaskExtractor(smartClient, ai.ExtractorConfig{
		UserName:       userName,
		Aliases:        cfg.User.Aliases,
		Language:       lang,
		SystemTemplate: overrides.extractorSystem,
		UserTemplate:   overrides.extractorUser,
		DateParse:      dateTimeCfg,
		ProjectsFn:     projectsFn,
		Now:            nowFn,
	})
	if err != nil {
		return aiComponents{}, fmt.Errorf("initializing extractor: %w", err)
	}

	return aiComponents{
		simpleClassifier: simpleClassifier,
		groupClassifier:  groupClassifier,
		extractor:        extractor,
		smartClient:      smartClient,
	}, nil
}

func buildTelegramComponents(cfg *config.Config, stateStore model.StateStore, guardStore model.GuardStore, hist model.History) (*tgbotapi.BotAPI, []model.Channel, []model.Notifier, error) {
	if cfg.Channels.Telegram == nil {
		return nil, nil, nil, nil
	}
	tgCfg := cfg.Channels.Telegram
	bot, err := tgbotapi.NewBotAPI(tgCfg.Token)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("initializing Telegram bot: %w", err)
	}

	tgChannel := channel.NewTelegramChannel(bot, channel.TelegramChannelConfig{
		OwnerIDs:        []string{fmt.Sprintf("%d", cfg.User.TelegramUserID)},
		OnJoin:          tgCfg.OnJoin,
		ReactionEnabled: tgCfg.ReactionEnabled,
		BotID:           fmt.Sprintf("%d", bot.Self.ID),
		BotUsername:     bot.Self.UserName,
		WelcomeMessage:  tgCfg.WelcomeMessage,
		ConfirmTimeout:  tgCfg.ConfirmTimeout,
		Language:        cfg.User.Language,
	}, stateStore, guardStore, hist, channel.HistoryConfig{})

	return bot, []model.Channel{tgChannel}, nil, nil
}

func buildDMNotifier(bot *tgbotapi.BotAPI, ownerID int64, lang string) (model.Notifier, error) {
	if bot != nil {
		bundle, err := huskwootI18n.NewBundle(lang)
		if err != nil {
			return nil, fmt.Errorf("i18n bundle: %w", err)
		}
		loc := huskwootI18n.NewLocalizer(bundle, lang)
		return sink.NewTelegramNotifier(bot, ownerID, loc), nil
	}
	return nil, nil
}

func buildIMAPChannel(cfg *config.Config, stateStore model.StateStore) model.Channel {
	imapConfigs := make([]channel.IMAPChannelConfig, 0, len(cfg.Channels.IMAP))
	for _, imapCfg := range cfg.Channels.IMAP {
		imapConfigs = append(imapConfigs, channel.IMAPChannelConfig{
			Host:           imapCfg.Host,
			Port:           imapCfg.Port,
			Username:       imapCfg.Username,
			Password:       imapCfg.Password,
			Folders:        imapCfg.Folders,
			Label:          imapCfg.Label,
			Senders:        imapCfg.Senders,
			OnFirstConnect: imapCfg.OnFirstConnect,
			PollInterval:   imapCfg.PollInterval,
		})
	}
	return channel.NewIMAPChannel(imapConfigs, stateStore)
}

func buildClassifiers(comps aiComponents) map[model.MessageKind]model.Classifier {
	return map[model.MessageKind]model.Classifier{
		model.MessageKindBatch: comps.simpleClassifier,
		model.MessageKindGroup: comps.groupClassifier,
	}
}

func buildExtractors(comps aiComponents) map[model.MessageKind]model.Extractor {
	return map[model.MessageKind]model.Extractor{
		model.MessageKindBatch: comps.extractor,
		model.MessageKindGroup: comps.extractor,
	}
}

func buildReminderScheduler(
	cfg *config.Config,
	bot *tgbotapi.BotAPI,
	taskStore model.TaskStore,
	weekdays []time.Weekday,
	loc *time.Location,
	nowFn func() time.Time,
	lang string,
	logger *slog.Logger,
) (*reminder.Scheduler, error) {
	if cfg.Reminders == nil {
		return nil, nil
	}

	parseHM := func(s string) (int, int, error) {
		t, err := time.Parse("15:04", s)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid time format %q (expected HH:MM): %w", s, err)
		}
		return t.Hour(), t.Minute(), nil
	}

	sched := cfg.Reminders.Schedule
	mh, mm, err := parseHM(sched.Morning)
	if err != nil {
		return nil, fmt.Errorf("reminders.schedule.morning: %w", err)
	}
	slots := []reminder.Slot{{Name: "morning", Hour: mh, Minute: mm}}

	if sched.Afternoon != "" {
		ah, am, err := parseHM(sched.Afternoon)
		if err != nil {
			return nil, fmt.Errorf("reminders.schedule.afternoon: %w", err)
		}
		slots = append(slots, reminder.Slot{Name: "afternoon", Hour: ah, Minute: am})
	}
	if sched.Evening != "" {
		eh, em, err := parseHM(sched.Evening)
		if err != nil {
			return nil, fmt.Errorf("reminders.schedule.evening: %w", err)
		}
		slots = append(slots, reminder.Slot{Name: "evening", Hour: eh, Minute: em})
	}

	if bot == nil {
		return nil, fmt.Errorf("reminder: channels.telegram not configured")
	}

	i18nBundle, err := huskwootI18n.NewBundle(lang)
	if err != nil {
		return nil, fmt.Errorf("i18n bundle: %w", err)
	}
	i18nLoc := huskwootI18n.NewLocalizer(i18nBundle, lang)
	deliverers := []model.SummaryDeliverer{
		sink.NewTelegramSummaryDeliverer(bot, cfg.User.TelegramUserID, i18nLoc),
	}

	builder := reminder.NewBuilder(taskStore, reminder.BuilderConfig{
		PlansHorizon: cfg.Reminders.PlansHorizon,
		UndatedLimit: cfg.Reminders.UndatedLimit,
	})

	return reminder.New(reminder.Config{
		Slots:         slots,
		SendWhenEmpty: cfg.Reminders.SendWhenEmpty,
	}, weekdays, loc, builder, deliverers, nowFn, logger), nil
}
