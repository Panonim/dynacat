# Installation

## Docker compose using provided directory structure (recommended)

Create a new directory called `dynacat` as well as the template files within it by running:

```bash
mkdir dynacat && cd dynacat && \
curl -sL https://github.com/glanceapp/docker-compose-template/archive/refs/heads/main.tar.gz | tar -xzf - --strip-components 2 && \
sed -i \
  -e 's/^  glance:/  dynacat:/' \
  -e 's/^    container_name: glance/    container_name: dynacat/' \
  -e 's/^    image: glanceapp\/glance/    image: panonim\/dynacat/' \
  docker-compose.yml && \
mv config/glance.yml config/dynacat.yml
```

> [!IMPORTANT]
> Remember to keep the command exactly as-is; otherwise, the image won't work.

*[click here to view the files that will be created](https://github.com/glanceapp/docker-compose-template/tree/main/root)*

Then, edit the following files as desired:
* `docker-compose.yml` to configure the port, volumes and other containery things
* `config/home.yml` to configure the widgets or layout of the home page
* `config/dynacat.yml` if you want to change the theme or add more pages

### Other files you may want to edit

* `.env` to configure environment variables that will be available inside configuration files
* `assets/user.css` to add custom CSS

When ready, run:

```bash
docker compose up -d
```

If you encounter any issues, you can check the logs by running:

```bash
docker compose logs
```

## Docker compose manual

Create a `docker-compose.yml` file with the following contents:

```yaml
services:
  dynacat:
    container_name: dynacat
    image: panonim/dynacat
    restart: unless-stopped
    volumes:
      - ./config:/app/config
      - ./assets:/app/assets
      - /etc/localtime:/etc/localtime:ro
      # Optionally, also mount docker socket if you want to use the docker containers widget
      # - /var/run/docker.sock:/var/run/docker.sock:ro
    ports:
      - 8080:8080
    env_file: .env
```

Then, create a new directories called `config` & `assets` and download the example starting [`dynacat.yml`](https://github.com/Panonim/dynacat/blob/main/docs/docs/dynacat.yml) file into it by running:

```bash
mkdir config && wget -O config/dynacat.yml https://raw.githubusercontent.com/Panonim/dynacat/refs/heads/main/docs/docs/dynacat.yml
```

Feel free to edit the `dynacat.yml` file to your liking, and when ready run:

```bash
docker compose up -d
```

If you encounter any issues, you can check the logs by running:

```bash
docker logs dynacat
```

## Coming from Glance

If you have already set up glance you're only one step away from switching to Dynacat!

All you have to do is replace your current image (`glanceapp/glance:latest`) with one from below:

```yaml
panonim/dynacat:latest
```

<details>
<summary>I get the following error: dynacat.yml: no such file or directory</summary>

<br>
Make sure to rename your `glance.yml` file into `dynacat.yml`.
</details>

### Disable automatic updates

| Variable | Default | Description |
| -------- | ------- | ----------- |
| `ENABLE_DYNAMIC_UPDATE` | `true` | Set to `false`, `0`, or `f` to disable automatic widget refresh. Useful for static views or default glance behaviour. |


## Build binary with Go

Requirements: [Go](https://go.dev/dl/) >= v1.23

To build the project for your current OS and architecture, run:

```bash
mkdir -p build && go build -o build/dynacat .
```

To build for a specific OS and architecture, run:

```bash
mkdir -p build && GOOS=linux GOARCH=amd64 go build -o build/dynacat .
```

[*click here for a full list of GOOS and GOARCH combinations*](https://go.dev/doc/install/source#:~:text=$GOOS%20and%20$GOARCH)

Alternatively, if you just want to run the app without creating a binary, like when you're testing out changes, you can run:

```bash
go run .
```