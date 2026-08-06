package main

import (
	"log"

	"github.com/spf13/viper"
)

func mustGet(key string) string {
	v := viper.GetString(key)
	if v == "" {
		log.Fatalf("required config key %q is not set", key)
	}
	return v
}

func get(key, def string) string {
	v := viper.GetString(key)
	if v == "" {
		return def
	}
	return v
}
