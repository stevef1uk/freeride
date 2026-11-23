package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func budgetKey(parts ...string) string { key, _ := json.Marshal(parts); return string(key) }

func updateBudgetLedger(path string, update func(map[string]int64) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("execution budget: %w", err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("execution budget: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("execution budget: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ledger := map[string]int64{}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &ledger); err != nil {
			return fmt.Errorf("execution budget: corrupt proxy ledger: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("execution budget: %w", err)
	}
	if ledger == nil {
		return fmt.Errorf("execution budget: null proxy ledger")
	}
	if err := update(ledger); err != nil {
		return err
	}
	data, err = json.Marshal(ledger)
	if err != nil {
		return fmt.Errorf("execution budget: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".proxy-budget-*")
	if err != nil {
		return fmt.Errorf("execution budget: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("execution budget: %w", err)
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("execution budget: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("execution budget: %w", err)
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("execution budget: %w", err)
	}
	return nil
}

func workflowBudgetPath() (string, error) {
	if path := os.Getenv("FREERIDE_EXECUTION_BUDGET_PATH"); path != "" {
		return path, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("execution budget: %w", err)
	}
	return filepath.Join(dir, "freeride", "execution-budgets.json"), nil
}
