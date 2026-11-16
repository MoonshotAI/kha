package commands

import (
	"github.com/spf13/cobra"

	kha "github.com/MoonshotAI/kha"
	"github.com/MoonshotAI/kha/internal/cli/output"
)

var environmentHeaders = []string{"ID", "NAME", "BUILD", "CREATED"}

func environmentRow(e *kha.Environment) []string {
	build := "-"
	if e.BuildStatus != nil {
		build = string(*e.BuildStatus)
	}
	return []string{e.ID, e.Name, build, output.Time(e.CreatedAt)}
}

// environmentWaitOptions projects the shared wait flags onto the Environment
// waiter, whose option type is distinct from the SDK's generic WaitOption.
func environmentWaitOptions(wf *waitFlags) []kha.EnvironmentWaitOption {
	return []kha.EnvironmentWaitOption{kha.WithEnvironmentTimeout(wf.timeout), kha.WithEnvironmentInterval(wf.interval)}
}

func newEnvironmentsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "environments",
		Short: "Manage execution Environments",
	}
	cmd.AddCommand(
		newEnvironmentsListCmd(g),
		newEnvironmentsGetCmd(g),
		newEnvironmentsCreateCmd(g),
		newEnvironmentsUpdateCmd(g),
		newEnvironmentsEditCmd(g),
		newEnvironmentsDeleteCmd(g),
		newEnvironmentsArchiveCmd(g),
		newEnvironmentsWaitReadyCmd(g),
		newEnvironmentsWorkCmd(g),
	)
	return cmd
}

// waitEnvironmentReady waits for a completed build and returns its latest model.
func waitEnvironmentReady(cmd *cobra.Command, client *kha.Client, id string, wf *waitFlags) (*kha.Environment, error) {
	stop := waitProgress(cmd, "waiting for environment %s build", id)
	result, err := client.Environments.WaitForReady(cmd.Context(), id, environmentWaitOptions(wf)...)
	stop()
	return result.Body, err
}

func newEnvironmentsWaitReadyCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "wait-ready <id>",
		Short:             "Wait for an Environment's build to become ready",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: environmentIDCompletion,
	}
	wf := addWaitFlags(cmd)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		env, err := waitEnvironmentReady(cmd, client, args[0], wf)
		if err != nil {
			return err
		}
		note(cmd, "environment %s is ready", env.ID)
		return printOne(cmd, g, environmentHeaders, environmentRow(env), env)
	})
	return cmd
}

func newEnvironmentsListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Environments",
		Args:  cobra.NoArgs,
	}
	lf := addListFlags(cmd)
	var includeArchived, officialOnly bool
	cmd.Flags().BoolVar(&includeArchived, "include-archived", false, "include archived Environments")
	cmd.Flags().BoolVar(&officialOnly, "official-only", false, "only official Environments provided by the platform")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListEnvironmentsRequest{PageSize: lf.pageSize(), PageToken: lf.token()}
		if includeArchived {
			params.IncludeArchived = new(true)
		}
		if officialOnly {
			params.OfficialOnly = new(true)
		}
		if lf.all {
			return printAll(cmd, g, environmentHeaders,
				client.Environments.All(cmd.Context(), params), environmentRow)
		}
		envsResult, err := client.Environments.List(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := envsResult.Body
		page.Items = nonNilItems(page.Items)
		if page.Items == nil {
			page.Items = []*kha.Environment{}
		}
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, e := range page.Items {
			rows = append(rows, environmentRow(e))
		}
		return printList(cmd, g, environmentHeaders, rows, page)
	})
	return cmd
}

func newEnvironmentsGetCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "get <id>",
		Short:             "Get one Environment",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: environmentIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			envResult, err := client.Environments.Get(cmd.Context(), kha.GetEnvironmentRequest{ID: args[0]})
			if err != nil {
				return err
			}
			env := envResult.Body

			return printOne(cmd, g, environmentHeaders, environmentRow(env), env)
		}),
	}
}

// newEnvironmentsCreateCmd creates a cloud or self-hosted Environment from
// flags. Cloud networking/packages/setup-script map to the common config
// knobs; the full config object remains available through --file.
func newEnvironmentsCreateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an Environment (cloud by default; --self-hosted for your own workers)",
		Args:  cobra.NoArgs,
		Example: `  kha environments create --name analysis
  kha environments create --name analysis --networking limited --allow-host example.com --pip pandas==2.3.2 --setup-script-file setup.sh
  kha environments create --name ci-runner --self-hosted`,
	}
	var name, description string
	var wait, selfHosted bool
	cmd.Flags().BoolVar(&selfHosted, "self-hosted", false, "run tools on your own workers instead of a cloud sandbox")
	cmd.Flags().StringVarP(&name, "name", "n", "", "environment name")
	cmd.Flags().StringVarP(&description, "description", "d", "", "description")
	cmd.Flags().BoolVarP(&wait, "wait", "w", false, "wait for the first version's build to become ready")
	var networking string
	cmd.Flags().StringVar(&networking, "networking", "", "network access for cloud sandboxes: limited or unrestricted")
	mustRegisterFlagCompletion(cmd, "networking", completeFlagStatic("limited", "unrestricted"))
	allowedHosts := addStrListFlag(cmd, "allow-host", "host allowed when --networking limited (repeatable)")
	allowPkgMgrs := cmd.Flags().Bool("allow-package-managers", false, "allow package manager egress when --networking limited")
	pip := addStrListFlag(cmd, "pip", "pip package spec to preinstall, e.g. --pip pandas==2.3.2 (repeatable)")
	npm := addStrListFlag(cmd, "npm", "npm package spec to preinstall (repeatable)")
	goPkgs := addStrListFlag(cmd, "go", "go module spec to preinstall (repeatable)")
	cargo := addStrListFlag(cmd, "cargo", "cargo crate spec to preinstall (repeatable)")
	setupScript := addTextFlags(cmd, "setup-script", "shell script run once when the sandbox starts")
	metadata := addKVFlag(cmd, "metadata", "key=value metadata entry (repeatable; up to 16)")
	wf := addWaitFlags(cmd)
	body := addBodyFlag(cmd, "name", "description", "self-hosted", "networking", "allow-host", "allow-package-managers", "pip", "npm", "go", "cargo", "setup-script", "setup-script-file", "metadata")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.CreateEnvironmentRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			if name == "" {
				return errUsage("environments create requires --name (or --file)")
			}
			if selfHosted && (cmd.Flags().Changed("networking") || cmd.Flags().Changed("allow-host") || cmd.Flags().Changed("allow-package-managers") || len(pip.out)+len(npm.out)+len(goPkgs.out)+len(cargo.out) > 0 || cmd.Flags().Changed("setup-script") || cmd.Flags().Changed("setup-script-file")) {
				return errUsage("--networking/--allow-host/--allow-package-managers/--pip/--npm/--go/--cargo/--setup-script only apply to cloud Environments; drop them or drop --self-hosted")
			}
			cfg := &kha.CloudEnvironmentConfig{Type: kha.CloudEnvironmentConfigTypeCloud}
			if cmd.Flags().Changed("networking") || len(allowedHosts.out) > 0 || *allowPkgMgrs {
				net := &kha.EnvironmentNetworking{Type: kha.NetworkAccess(networking)}
				if !cmd.Flags().Changed("networking") {
					net.Type = kha.NetworkAccessLimited
				}
				for _, h := range allowedHosts.out {
					net.AllowedHosts = append(net.AllowedHosts, kha.AllowedHost(h))
				}
				if *allowPkgMgrs {
					net.AllowPackageManagers = allowPkgMgrs
				}
				cfg.Networking = net
			}
			if len(pip.out)+len(npm.out)+len(goPkgs.out)+len(cargo.out) > 0 {
				pkgs := &kha.EnvironmentPackages{}
				for _, s := range pip.out {
					pkgs.Pip = append(pkgs.Pip, kha.PackageSpec(s))
				}
				for _, s := range npm.out {
					pkgs.Npm = append(pkgs.Npm, kha.PackageSpec(s))
				}
				for _, s := range goPkgs.out {
					pkgs.Go = append(pkgs.Go, kha.PackageSpec(s))
				}
				for _, s := range cargo.out {
					pkgs.Cargo = append(pkgs.Cargo, kha.PackageSpec(s))
				}
				cfg.Packages = pkgs
			}
			script, err := setupScript.resolve(cmd)
			if err != nil {
				return err
			}
			cfg.SetupScript = script
			req = kha.CreateEnvironmentRequest{Name: name, Config: *kha.NewEnvironmentConfigCloudEnvironmentConfig(cfg)}
			if selfHosted {
				local := &kha.SelfHostedEnvironmentConfig{Type: kha.SelfHostedEnvironmentConfigTypeSelfHosted}
				req.Config = *kha.NewEnvironmentConfigSelfHostedEnvironmentConfig(local)
			}
			if cmd.Flags().Changed("description") {
				req.Description = new(description)
			}
			req.Metadata = metadata.mapValue()
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		envResult, err := client.Environments.Create(cmd.Context(), kha.CreateEnvironmentHTTPRequest{Body: &req})
		if err != nil {
			return err
		}
		env := envResult.Body

		cfg := env.Config
		if cfg.AsSelfHostedEnvironmentConfig() != nil {
			note(cmd, "environment %s created (self-hosted; no build required)", env.ID)
		} else {
			note(cmd, "environment %s created (build started)", env.ID)
		}
		if wait {
			env, err = waitEnvironmentReady(cmd, client, env.ID, wf)
			if err != nil {
				return err
			}
			note(cmd, "environment %s is ready", env.ID)
		}
		return printOne(cmd, g, environmentHeaders, environmentRow(env), env)
	})
	return cmd
}

func newEnvironmentsUpdateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "update <id>",
		Short:             "Update an Environment, creating a new version",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: environmentIDCompletion,
		Example: `  kha environments update env_xxx --description "analysis with pandas"
  kha environments update env_xxx --config '{type: cloud, networking: {type: limited, allowed_hosts: [example.com]}}'`,
	}
	var name, description string
	cmd.Flags().StringVarP(&name, "name", "n", "", "new name")
	cmd.Flags().StringVarP(&description, "description", "d", "", "new description (empty clears)")
	var config kha.EnvironmentConfig
	cmd.Flags().Var(&yamlValue{flag: "config", target: &config}, "config", "replacement execution configuration as a JSON or YAML object (creates a new immutable version)")
	metadata := addKVFlag(cmd, "metadata", "key=value metadata entry (repeatable; replaces the whole map)")
	body := addBodyFlag(cmd, "name", "description", "config", "metadata")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.UpdateEnvironmentRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			changed := false
			if cmd.Flags().Changed("name") {
				req.Name = new(name)
				changed = true
			}
			if cmd.Flags().Changed("description") {
				req.Description = new(description)
				changed = true
			}
			if cmd.Flags().Changed("config") {
				req.Config = &config
				changed = true
			}
			if m := metadata.mapValue(); m != nil {
				req.Metadata = m
				changed = true
			}
			if !changed {
				return errUsage("nothing to update: pass at least one field flag or --file")
			}
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		envResult, err := client.Environments.Update(cmd.Context(), kha.UpdateEnvironmentHTTPRequest{ID: args[0], Body: &req})
		if err != nil {
			return err
		}
		env := envResult.Body

		note(cmd, "environment %s updated", env.ID)
		return printOne(cmd, g, environmentHeaders, environmentRow(env), env)
	})
	return cmd
}

func newEnvironmentsDeleteCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "delete <id>",
		Short:             "Delete an Environment",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: environmentIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Environments.Delete(cmd.Context(), kha.DeleteEnvironmentRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "environment %s deleted", args[0])
			return nil
		}),
	}
}

func newEnvironmentsArchiveCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "archive <id>",
		Short:             "Archive an Environment (no new Sessions; existing ones unaffected)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: environmentIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Environments.Archive(cmd.Context(), kha.ArchiveEnvironmentRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "environment %s archived", args[0])
			return nil
		}),
	}
}
