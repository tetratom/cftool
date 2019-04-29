package main

import (
	"github.com/aws/aws-sdk-go/service/sts"
	"log"
	"os"
	"strings"
)

// Whoami prints information about the caller's AWS principal.
func (prog *Program) Whoami(args []string) error {
	if len(args) > 1 {
		log.Printf("expected no additional arguments in: %s\n", strings.Join(args, " "))
		os.Exit(1)
	}

	identity, err := prog.getWhoami()
	if err != nil {
		return err
	}

	PPrintWhoami(prog.AWS.Session(), identity)

	return nil
}

func (prog *Program) getWhoami() (*sts.GetCallerIdentityOutput, error) {
	client := sts.New(prog.AWS.Session())
	identity, err := client.GetCallerIdentity(&sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, err
	}

	return identity, nil
}
