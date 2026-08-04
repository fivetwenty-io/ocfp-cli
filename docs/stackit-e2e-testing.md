# OCFP runbook for Stackit IaaS

In the following lines we will go through the the commands required to deploy OCFP under the Stackit IaaS provider

## Prerequisites

1. Docker https://docs.docker.com/get-started/get-docker/ (only for the dev container path)

confirm by checking the current ccontainers available

```shell
docker ps -a
```

2. [Go](https://go.dev/doc/install) (Only for the golang path of the release)

cofnirm by running

```shell
go version
```

3. Git [user](https://docs.github.com/en/get-started/git-basics/setting-your-username-in-git)

confirm by grabbing your user info

```shell
git config user.name
```
4. ssh-agent user identity added that has access to deployments [repository](https://github.com/stackitcloud/scf-ocfp-bosh-deployments)

confirm by running

```shell
ssh -T git@gtihub.com
```
and

```shell
git ls-remote git@github.com:stackitcloud/scf-ocfp-bosh-deployments.git
```
5. stackit cli - [instructions](https://github.com/stackitcloud/stackit-cli/blob/main/INSTALLATION.md)

confirm by running

```shell
stackit -v
```
6. authenticated gihub (gh) [cli](https://cli.github.com/) that has access to the ocfp-cli [releases](https://github.com/ocfp/ocfp-cli/releases)

```shell
gh version
```
and
```shell
gh release list --repo ocfp/ocfp-cli
```
and for the go version

```shell
gh repo view fivetwenty-io/ocfp-cli-go
```

## OCFP Cli installation (dev container - skip in favor of the release path below)

* Clone the repository

```shell
git clone git@github.com:ocfp/ocfp-cli.git
```

* cd into the cloned directory

```shell
cd ocfp-cli
```

* Run the makefile creator

```shell
make clean
```

* Create the container that will handle the installation

```shell
make dev
```

*Hint*

If you find yourself exiting the container and you'd like to create it from scratch make sure that you run `make clean` on your local machine first before you run `make dev`

### inside the OCFP Container

* Update the Makefile for the linux distro

```shell
make clean
```

* Install all dependencies

```shell
make vendor
```

* Activate Genesis dependency

```shell
g -v
```

## OCFP Cli installation (perl release - skip in favor of the go release below)

* download the release
macos - apple silicon
```shell
gh release download --repo ocfp/ocfp-cli --pattern "*2level.tar.gz"
```
```shell
gh release download --repo ocfp/ocfp-cli --pattern "*linux.tar.gz"
```

* untar the release

```shell
tar -xzf *.tar.gz
```

* cd into the directory

```shell
cd release
```

* install the cli

```shell
./install
```

* confirm installation

```shell
ocfp -v
```

## OCFP Cli installation (go release)

* clone the repository
macOS - apple silicon
```shell
gh repo clone fivetwenty-io/ocfp-cli-go -- --branch dev
```

* cd into the directory

```shell
cd ocfp-cli-go

```

* build  the cli

```shell
make build
```

* confirm built release

```shell
build/ocfp-darwin-arm64 -h
```

## Bootstrap

The bootstrap step is reposnsible for getting all the resources ready (includingg the bastion) under the IaaS.

### Configuration file

It utilizes a configuration file in yaml format that provides the required information for the bloc we are creating. It looks for its presence under ~/.config/ocfp/bloc_name.yml. For example:

```yml
---
blocs:
  - name: scf-stackit-eu01-004-dev                                          # The bloc name
    provider: stackit                                                       # TThe provider we are deploying to
    deployments:
      url: git@github.com:stackitcloud/scf-ocfp-bosh-deployments            # The git deployments URL
    project_id: "b7b6909a-a151-4381-aadb-f83113027f41"                      # The project id (provider specific)
    org_id: "493fe1ba-9ceb-400f-9506-faf3ec566421"                          # The org id (provider specific)
    service_account_json: '{}'                                              # The servcie account credentials in json format (provider specific)
    fqdns:                                                                  # The FQDNS of each of the deployed resources
      base:
        - "004.dev.scf.eu01.stackit.cloud"
      mgmt: # {} # base, concourse, prometheus, ...
        shield: "shield.util.004.dev.scf.eu01.stackit.cloud"
        concourse: "concourse.util.004.dev.scf.eu01.stackit.cloud"
        doomsday: "doomsday.util.004.dev.scf.eu01.stackit.cloud"

      ocf:
        system: "system.004.dev.scf.eu01.stackit.cloud"
        apps: "apps.004.dev.scf.eu01.stackit.cloud"
        base: "004.dev.scf.eu01.stackit.cloud"
        stratos: "console.apps.004.dev.scf.eu01.stackit.cloud"
        alertmanager: "alertmanager.util.004.dev.scf.eu01.stackit.cloud"
        prometheus: "prometheus.util.004.dev.scf.eu01.stackit.cloud"
        grafana: "grafana.util.004.dev.scf.eu01.stackit.cloud"

    s3:                                                                     # The s3 credentials for the bucket creation
      access_key_id: ""
      secret_access_key: ""

    allowed_ingress_ips:                                                    # The allowed ingress ip's for tha bastion
      - '96.243.23.50'  # FiveTwenty Office, Buffalo, NY, USA
      - '96.243.23.51'  # FiveTwenty Office, Buffalo, NY, USA
      - '207.81.22.46'  # FiveTwenty Office, Vancouver, BC, Canada
      - '207.81.75.145' # FiveTwenty Office, Vancouver, BC, Canada
      - '45.56.81.156'  # FiveTwenty Lab CA, Vancouver, BC, Canada
      - '82.76.166.230' # FiveTwenty Lab Europe, Romania
      - '109.178.173.38'  # FiveTwenty Lab Europe, Greece
```

#### Aqcuiring information for your config file (provider specific)

##### Project Details
---

* visit [portal.stackit.cloud](https://portal.stackit.cloud/dashboard)

Select the project you will be deploying your OCFP platform on.

Under Project infromation you'll find:

`project_id`

`org_id`

##### Service Account
---
* click on Service accounts from the side bar

* click on the "+ Create ervice account" button on the top of the screen

* type the email prefix and click "Create"

keep a note on resulting email accout for example an email prefix of `ocfp-runbook` would create something similar to

`ocfp-runbook-q3x9jv1@sa.stackit.cloud`

* click on the newly selected Service Account

* click on the "Service Account Keys" From the side bar

* click on "+ Create service account key"

* click on "Create new key pair" from the top selections

* click on "Copy" and select "Copy JSON" from the drop down menu

You can now paste the contents next to:

`service_account_json`

```json
{"id":"","publicKey":"-----BEGIN PUBLIC KEY----------END PUBLIC KEY-----","createdAt":"2025-10-21T11:43:21.637+00:00","keyType":"USER_MANAGED","keyOrigin":"GENERATED","keyAlgorithm":"RSA_2048","active":true,"credentials":{"kid":"","iss":"","sub":"","aud":"","privateKey":"-----BEGIN PRIVATE KEY----------END PRIVATE KEY-----"}}
```

*Important*

Make sure you have the editor role for the service account you created, or use a service account that already has it. To confirm the role assigned to the service account click on "Access" under "IAM AND MANAGEMENT" on the sidebar and paste the email address of the service account you kept a note from earlier. If you have the correct rights you can grant the editor role by clicking "Roles" stil under "IAM AND MANAGEMENT" on the sidebar and on the right, expand the Basic roles and by clicking on the editor menu on the far right, select "Grant access" and use the service account email you created.

##### FQDNS
---
The FQDNS information is constructed based on the base. Depending on your project's status dev/qa/prod you can update the base and replace each occurence under each endpoint/service.

##### s3
---
The S3 details can be found/created through the object Storage side bar menu

* click on "Object Storage" under "COMPUTING"

* click on "Credentials & Groups"

* click on "+ Create credentials group" or use a group already in place

* click on the Credential Group

* click on "Credentials" from the Side Bar Menu

* click on the "+ Create credentials" button on the top of the screen

here you will find the values for

`access_key_id`

`secret_access_key`

make sure you keep a note

##### Ingress IPs

The bootstrap process will create all the underlying resources including the bastion and its corresponding security groups in this section you'd like to populate with a list of IPs that will access the bastion. Make sure you include the one you are currently using

```shell
curl ifconfig.me
```

#### Location
---

Once you have the complete file contents you can place them under:

```shell
~/.config/ocfp/bloc_name.yml
```

### running bootstrap

* confirm/cd back into the deflated tar directory

```shell
cd release
```

* bootstrap the bloc name you defined on your config file

```shell
bin/ocfp --bloc-name bloc_name bootstrap
```

## through the bastion

With the boostrap step above complete we are ready to ssh and continue the process through the bastion

* ssh into the bastion

```shell
bin/ocfp --bloc-name bloc_name ssh
```

* confirm app/vault access and folder structure:

  * ocfp
  ```shell
  ocfp -h
  ```
  * genesis
  ```shell
  g -v
  ```
  * folder structure
  ```shell
  ls ocfp/deployments/
  ```
  should produce a list of the deploymetns direcory
  ```shell
  README.md  autoscaler  blacksmith  bosh  cf  concourse  doomsday  jumpbox  prometheus  scheduler  shield  vault
  ```
  * vault targets
  ```shell
  safe targets
  ```
  should produce the inception/in-memmory vault
  ```shell
  Known Vault targets - current target indicated with a (*):
  (*) scf-stackit-eu01-004-dev-inception   (insecure) http://127.0.0.1:8234
  ```
* start and attach to the tmux session

```shell
ocfp tmux
```

```shell
tmux attach -t ocfp
```

## Deployments

Using the optional ocfp tmux session above you can use each of the windows (one per deployment) to track the progress of each of the deployments

### mgmt bosh

* deploy

```shell
g @dev-mgmt:bosh deploy -F -y
```

* info (grab deployment info)

```shell
g @dev-mgmt:bosh info
```

* interact with the bosh cli (i.e. deps)

```shell
g @dev-mgmt:bosh b deps
```

### mgmt vault

* deploy

```shell
g @dev-mgmt:vault deploy -F -y
```

* initialize

```shell
g @dev-mgmt:vault do i
```
* info (grab deployment info)

```shell
g @dev-mgmt:vault info
```

### inception vault migrate

* migrate (requires user input)

```shell
ocfp vault migrate
```

* confirm updated targets

```shell
safe targets
```

### mgmt shield

* deploy

```shell
g @dev-mgmt:shield deploy -F -y
```

* create and push runtime config (requires user input)

```shell
g @dev-mgmt:shield do rc
```
* info (grab deployment info)

```shell
g @dev-mgmt:shield info
```

### mgmt doomsday

* setup approle for doomsday access (requires user input)

```shell
g @dev-mgmt:doomsday do setup-approle
```

* deploy

```shell
g @dev-mgmt:doomsday deploy -F -y
```

* info (grab deployment info)

```shell
g @dev-mgmt:doomsday info
```

### mgmt concourse

* setup approle for doomsday access (requires user input)

```shell
g @dev-mgmt:concourse do setup-approle
```

* deploy

```shell
g @dev-mgmt:concourse deploy -F -y
```

* info (grab deployment info)

```shell
g @dev-mgmt:concourse info
```
