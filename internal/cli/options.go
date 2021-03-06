package cli

import (
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/cloudformation/cloudformationiface"
	"github.com/aws/aws-sdk-go/service/sts"
	"github.com/aws/aws-sdk-go/service/sts/stsiface"
	"github.com/pborman/getopt/v2"
	"github.com/pkg/errors"
	"github.com/tetratom/cftool/internal"
	"os"
	"time"
)

type GlobalOptions struct {
	AWS           AWSOptions
	Color         bool
	Version       bool
	remainingArgs []string
}

type AWSOptions struct {
	Profile  string
	Region   string
	Endpoint string

	sess *session.Session
	cfn  cloudformationiface.CloudFormationAPI
	sts  stsiface.STSAPI
}

func (awsOpts *AWSOptions) Session() (*session.Session, error) {
	if awsOpts.sess == nil {
		opts := session.Options{}
		opts.SharedConfigState = session.SharedConfigEnable
		opts.AssumeRoleTokenProvider = stscreds.StdinTokenProvider
		opts.AssumeRoleDuration = 1 * time.Hour // todo: configurable?

		if awsOpts.Profile != "" {
			opts.Profile = awsOpts.Profile
		}

		if awsOpts.Region != "" {
			opts.Config.Region = aws.String(awsOpts.Region)
		}

		sess, err := session.NewSessionWithOptions(opts)
		if err != nil {
			return nil, errors.Wrap(err, "create aws session")
		}

		creds, err := internal.WrapCredentialsWithCache(opts.Profile, sess.Config.Credentials)
		if err != nil {
			return nil, errors.Wrap(err, "credential cache")
		}

		sess.Config.Credentials = creds

		awsOpts.sess = sess
	}

	return awsOpts.sess, nil
}

func (awsOpts *AWSOptions) CloudFormationClient(region string) (cloudformationiface.CloudFormationAPI, error) {
	if awsOpts.cfn == nil {
		sess, err := awsOpts.Session()
		if err != nil {
			return nil, err
		}

		var config []*aws.Config
		if awsOpts.Endpoint != "" {
			config = append(config, &aws.Config{Endpoint: &awsOpts.Endpoint})
		}

		if region != "" {
			config = append(config, &aws.Config{Region: &region})
		}

		awsOpts.cfn = cloudformation.New(sess, config...)
	}

	return awsOpts.cfn, nil
}

func (awsOpts *AWSOptions) STSClient() (stsiface.STSAPI, error) {
	if awsOpts.sts == nil {
		sess, err := awsOpts.Session()
		if err != nil {
			return nil, err
		}

		awsOpts.sts = sts.New(sess)
	}

	return awsOpts.sts, nil
}

func ParseGlobalOptions(args []string) GlobalOptions {
	var options GlobalOptions

	flags := getopt.New()
	flags.FlagLong(&options.AWS.Region, "region", 'r', "AWS region")
	flags.FlagLong(&options.AWS.Profile, "profile", 'p', "AWS credential profile")
	flags.FlagLong(&options.AWS.Endpoint, "endpoint", 'e', "AWS API endpoint")
	showHelp := flags.BoolLong("help", 'h', "show usage and exit")
	color := flags.EnumLong(
		"color", 'c', []string{"on", "off"}, "on",
		"'on' or 'off'. pass 'off' to disable colors.")
	flags.FlagLong(&options.Version, "version", 'V', "show version and exit")
	flags.SetProgram("cftool")
	flags.Parse(args)
	options.Color = color == nil || *color == "on"
	options.remainingArgs = flags.Args()

	if *showHelp {
		flags.PrintUsage(os.Stdout)
		os.Exit(0)
	}

	return options
}

type DeployOptions struct {
	Yes          bool
	ManifestFile string
	Stack        string
	Tenant       string
	ShowDiff     bool
}

func ParseDeployOptions(args []string) DeployOptions {
	var options DeployOptions

	flags := getopt.New()
	flags.FlagLong(&options.Yes, "yes", 'y', "do not prompt for confirmation")
	flags.FlagLong(&options.ManifestFile, "manifest", 'f', "manifest path")
	flags.FlagLong(&options.Stack, "stack", 's', "stack to deploy")
	flags.FlagLong(&options.Tenant, "tenant", 't', "tenant to deploy for")
	showDiff := flags.BoolLong("diff", 'd', "show template diff when updating a stack")
	showHelp := flags.BoolLong("help", 'h', "show usage and exit")
	flags.SetProgram("cftool [options ...] deploy")
	flags.Parse(args)
	options.ShowDiff = *showDiff
	rest := flags.Args()

	if len(rest) != 0 {
		fmt.Printf("error: did not expect positional parameters.\n")
		flags.PrintUsage(os.Stdout)
		os.Exit(1)
	}

	if *showHelp {
		flags.PrintUsage(os.Stdout)
		os.Exit(0)
	}

	return options
}

type UpdateOptions struct {
	Parameters     []string
	ParameterFiles []string
	Yes            bool
	StackName      string
	TemplateFile   string
	ShowDiff       bool
}

func ParseUpdateOptions(args []string) UpdateOptions {
	var options UpdateOptions

	flags := getopt.New()
	flags.FlagLong(&options.Parameters, "parameter", 'P', "explicit parameters")
	flags.FlagLong(&options.ParameterFiles, "parameter-file", 'p', "path to parameter file")
	flags.FlagLong(&options.Yes, "yes", 'y', "do not prompt for update confirmation (if a stack already exists)")
	flags.FlagLong(&options.StackName, "stack-name", 'n', "override inferrred stack name")
	flags.FlagLong(&options.TemplateFile, "template-file", 't', "template file")
	showDiff := flags.BoolLong("diff", 'd', "show template diff when updating a stack")
	showHelp := flags.BoolLong("help", 'h', "show usage and exit")
	flags.SetProgram("cftool [options ...] update")
	flags.Parse(args)
	options.ShowDiff = *showDiff
	rest := flags.Args()

	if len(rest) != 0 {
		fmt.Print("error: did not expect positional parameters\n")
		flags.PrintUsage(os.Stdout)
		os.Exit(1)
	}

	if *showHelp {
		flags.PrintUsage(os.Stdout)
		os.Exit(0)
	}

	return options
}
