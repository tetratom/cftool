package internal

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	cf "github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/cloudformation/cloudformationiface"
	"github.com/aws/aws-sdk-go/service/sts"
	"github.com/aws/aws-sdk-go/service/sts/stsiface"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/pmezard/go-difflib/difflib"
	"github.com/tetratom/cftool/pkg/cftool"
	"github.com/tetratom/cftool/pkg/pprint"
	"io"
	"strings"
	"time"
)

var ErrAbortedByUser = errors.New("aborted by user")

type StackStatus string

func (status StackStatus) IsComplete() bool {
	return strings.HasSuffix(string(status), "_COMPLETE")
}

func (status StackStatus) IsFailed() bool {
	return strings.HasSuffix(string(status), "_FAILED")
}

func (status StackStatus) IsTerminal() bool {
	return status.IsComplete() || status.IsFailed()
}

type Deployer struct {
	*cftool.Deployment
	client        cloudformationiface.CloudFormationAPI
	ChangeSetName string
	ShowDiff      bool
}

func NewDeployer(api cloudformationiface.CloudFormationAPI, d *cftool.Deployment) *Deployer {
	return &Deployer{
		Deployment: d,
		client:     api,
	}
}

func (d *Deployer) Deploy(c context.Context, w io.Writer) error {
	pprint.Field(w, "StackName", d.StackName)

	exists, err := d.stackExists()
	if err != nil {
		return errors.Wrapf(err, "describe stack %s", d.StackName)
	}

	if !exists {
		if !pprint.Promptf(w, "\nStack %s does not exist. Create?", d.StackName) {
			return ErrAbortedByUser
		}
	}

	if exists && d.ShowDiff {
		err := d.TemplateDiff(w)
		if err != nil {
			return errors.Wrap(err, "template diff")
		}
	}

	nochange := false
	chset, err := d.createChangeSet(!exists)
	if err != nil {
		if strings.Contains(err.Error(), "The submitted information didn't contain changes") {
			nochange = true
		} else {
			return errors.Wrap(err, "create change set")
		}
	}

	if nochange {
		fmt.Fprintf(w, "\nNo change.\n")
	} else {
		pprint.ChangeSet(w, chset)

		if d.Protected && !pprint.Promptf(w, "\nExecute change set?") {
			return ErrAbortedByUser
		}

		if chset == nil {
			return errors.New("expected non-nil chset")
		}

		since := time.Now()

		_, err = d.client.ExecuteChangeSet(
			&cf.ExecuteChangeSetInput{
				StackName:     chset.StackName,
				ChangeSetName: chset.ChangeSetName,
			})
		if err != nil {
			return errors.Wrap(err, "execute change set")
		}

		stack, err := d.monitorStackUpdate(w, since)
		if err != nil {
			return errors.Wrap(err, "monitor stack update")
		}

		status := StackStatus(*stack.StackStatus)
		if !exists && status == cf.StackStatusRollbackComplete {
			if pprint.Promptf(w, "\nStack failed creation, and must be deleted. Continue?") {
				_, err := d.client.DeleteStack(&cf.DeleteStackInput{
					StackName: chset.StackName,
				})

				if err != nil {
					return errors.Wrap(err, "delete failed stack")
				}

				_, err = d.monitorStackUpdate(w, time.Now())

				if err != nil {
					return errors.Wrap(err, "monitor stack delete")
				}

				return nil
			}
		}
	}

	outputs, err := d.getStackOutputs()
	if err != nil {
		return errors.Wrap(err, "get stack outputs")
	}

	for i, output := range outputs {
		if i == 0 {
			fmt.Fprintf(w, "\n")
		}

		pprint.StackOutput(w, output)
	}

	return nil
}

func (d *Deployer) describeStack() (*cf.Stack, error) {
	stacks, err := d.client.DescribeStacks(
		&cf.DescribeStacksInput{StackName: aws.String(d.StackName)})

	if err != nil {
		return nil, errors.Wrapf(err, "describe stack %s", d.StackName)
	}

	if len(stacks.Stacks) != 1 {
		return nil, errors.Wrapf(err, "stack %s not found", d.StackName)
	}

	return stacks.Stacks[0], nil
}

func (d *Deployer) stackExists() (bool, error) {
	_, err := d.describeStack()
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return false, nil
		}

		return false, err
	}

	return true, err
}

func (d *Deployer) createChangeSet(create bool) (*cf.DescribeChangeSetOutput, error) {
	changeSetType := cf.ChangeSetTypeUpdate
	if create {
		changeSetType = cf.ChangeSetTypeCreate
	}

	d.ChangeSetName = "StackUpdate-" + uuid.New().String()

	input := cf.CreateChangeSetInput{
		StackName:     aws.String(d.StackName),
		ChangeSetName: aws.String(d.ChangeSetName),
		Parameters:    make([]*cf.Parameter, len(d.Parameters)),
		TemplateBody:  aws.String(string(d.TemplateBody)),
		ChangeSetType: aws.String(changeSetType),
		Capabilities: []*string{
			aws.String("CAPABILITY_IAM"),
			aws.String("CAPABILITY_NAMED_IAM"),
		},
	}

	index := 0
	for key, value := range d.Parameters {
		input.Parameters[index] = &cf.Parameter{
			ParameterKey:   aws.String(key),
			ParameterValue: aws.String(value),
		}

		index += 1
	}

	_, err := d.client.CreateChangeSet(&input)
	if err != nil {
		return nil, err
	}

	var chset *cf.DescribeChangeSetOutput

	for done := false; !done; {
		// It's probably not going to be ready immediately anyway, so let's wait
		// at the start of the loop.
		time.Sleep(2 * time.Second)

		chset, err = d.client.DescribeChangeSet(
			&cf.DescribeChangeSetInput{
				StackName:     aws.String(d.StackName),
				ChangeSetName: aws.String(d.ChangeSetName),
			})
		if err != nil {
			return nil, errors.Wrap(err, "describe change set")
		}

		switch *chset.Status {
		case cf.ChangeSetStatusCreateComplete:
			done = true

		case cf.ChangeSetStatusFailed:
			return nil, errors.Errorf(
				"failed to create change set: %s", *chset.StatusReason)

		case cf.ChangeSetStatusDeleteComplete:
			return nil, errors.New("change set removed unexpectedly")
		}
	}

	return chset, nil
}

func (d *Deployer) getStackEvents(since time.Time, until time.Time) ([]*cf.StackEvent, error) {
	out, err := d.client.DescribeStackEvents(
		&cf.DescribeStackEventsInput{
			StackName: aws.String(d.StackName),
		})
	if err != nil {
		return nil, errors.Wrap(err, "describe stack events")
	}

	result := make([]*cf.StackEvent, 0, len(out.StackEvents))
	for _, event := range out.StackEvents {
		if (event.Timestamp.After(since) || event.Timestamp.Equal(since)) &&
			event.Timestamp.Before(until) {

			result = append(result, event)
		}
	}

	return result, nil
}

func (d *Deployer) getStackOutputs() ([]*cf.Output, error) {
	stack, err := d.client.DescribeStacks(
		&cf.DescribeStacksInput{
			StackName: aws.String(d.StackName),
		})
	if err != nil {
		return nil, errors.Wrap(err, "describe stack")
	}

	return stack.Stacks[0].Outputs, nil
}

func (d *Deployer) monitorStackUpdate(w io.Writer, startTime time.Time) (stack *cf.Stack, err error) {
	lastStatus := StackStatus("UNKNOWN")
	since := startTime

	for i := 0; ; i++ {
		stack, err = d.describeStack()
		if err != nil {
			return nil, err
		}

		if stack == nil {
			return nil, errors.New("unexpected nil stack")
		}

		status := StackStatus(*stack.StackStatus)

		if status != lastStatus {
			fmt.Fprintf(w, "\n")
			t := time.Now()
			events, err := d.getStackEvents(since, t)
			since = t
			if err != nil {
				return nil, errors.Wrap(err, "get stack events")
			}

			for _, event := range events {
				if strings.HasSuffix(*event.ResourceStatus, "_FAILED") ||
					strings.HasSuffix(*event.ResourceStatus, "_ROLLBACK_IN_PROGRESS") {

					pprint.StackEvent(w, event)
				}
			}

			lastStatus, i = status, 0
			fmt.Fprintf(w, "%s", status)

			if !status.IsTerminal() {
				fmt.Fprintf(w, "...")
			}
		}

		if status.IsTerminal() {
			fmt.Fprintf(w, "\n")
			break
		}

		sleepTime := 5 * time.Second

		if i < 5 {
			// Rapid updates for the first 10 seconds.
			sleepTime = 2 * time.Second
		}

		time.Sleep(sleepTime)
		fmt.Fprintf(w, ".")
	}

	return stack, err
}

func (d *Deployer) Whoami(w io.Writer, api stsiface.STSAPI, region string) (*sts.GetCallerIdentityOutput, error) {
	// todo: replace this with something better

	id, err := api.GetCallerIdentity(&sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, err
	}

	pprint.Whoami(w, &region, id)
	return id, nil
}

func (d *Deployer) TemplateDiff(w io.Writer) error {
	fmt.Fprintf(w, "\n")

	exists, err := d.stackExists()

	switch {
	case err != nil:
		return errors.Wrapf(err, "describe stack %s", d.StackName)

	case !exists:
		return errors.Errorf("stack %s does not exist.", d.StackName)
	}

	out, err := d.client.GetTemplate(&cf.GetTemplateInput{
		StackName: aws.String(d.StackName),
	})

	if err != nil {
		return errors.Wrap(err, "get template")
	}

	diff := difflib.UnifiedDiff{
		A: difflib.SplitLines(*out.TemplateBody),
		B: difflib.SplitLines(
			strings.ReplaceAll(
				string(d.TemplateBody), "\r", "")),
		FromFile: "",
		ToFile:   "",
		Context:  0,
	}

	text, err := difflib.GetUnifiedDiffString(diff)
	if err != nil {
		return errors.Wrap(err, "unified diff")
	}

	lines := strings.Split(text, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if len(line) < 1 {
			continue
		}

		col := pprint.ColDiffText

		switch line[0] {
		case '@':
			col = pprint.ColDiffHeader
		case '+':
			col = pprint.ColDiffAdd
		case '-':
			col = pprint.ColDiffRemove
		}

		_, _ = col.Fprint(w, line)

		fmt.Fprintf(w, "\n")
	}

	return nil
}
