package util

import (
	"log/slog"
	"os"
	"strings"

	"github.com/kamikazechaser/util/logg"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

func InitLogger() *slog.Logger {
	loggOpts := logg.Opts{
		Component:  "clc-core",
		FormatType: logg.Logfmt,
		LogLevel:   slog.LevelInfo,
	}

	if os.Getenv("DEBUG") != "" {
		loggOpts.LogLevel = slog.LevelDebug
	}

	if os.Getenv("DEV") != "" {
		loggOpts.LogLevel = slog.LevelDebug
		loggOpts.FormatType = logg.Human
	}

	if os.Getenv("LOG_FORMAT") == "json" {
		loggOpts.FormatType = logg.JSON
	}

	return logg.NewLogg(loggOpts)
}

func InitConfig(lo *slog.Logger, confFilePath string) *koanf.Koanf {
	var (
		ko = koanf.New(".")
	)

	confFile := file.Provider(confFilePath)
	if err := ko.Load(confFile, toml.Parser()); err != nil {
		if os.IsNotExist(err) {
			lo.Warn("config file not found; relying on CORE_* env vars", "path", confFilePath)
		} else {
			lo.Error("could not parse configuration file", "error", err)
			os.Exit(1)
		}
	}

	if err := ko.Load(env.ProviderWithValue("CORE_", ".", func(key string, value string) (string, any) {
		mappedKey := strings.ReplaceAll(strings.ToLower(
			strings.TrimPrefix(key, "CORE_")), "__", ".")
		// Convert escaped newlines so multiline values (e.g. PEM signing keys) can
		// be supplied via env vars. docker env_file passes "\n" as two literal bytes;
		// pem.Decode splits on 0x0A and rejects the literal sequence. Convert before
		// array splitting so PEM headers like "PRIVATE KEY" aren't mistaken for a
		// space-separated array.
		value = strings.ReplaceAll(value, "\\n", "\n")

		if strings.Contains(value, "\n") {
			return mappedKey, value
		}
		if strings.Contains(value, ",") {
			return mappedKey, strings.Split(value, ",")
		}
		if strings.Contains(value, " ") {
			return mappedKey, strings.Split(value, " ")
		}
		return mappedKey, value
	}), nil); err != nil {
		lo.Error("could not override config from env vars", "error", err)
		os.Exit(1)
	}

	if os.Getenv("DEBUG") != "" || os.Getenv("DEV") != "" {
		ko.Print()
	}

	return ko
}
