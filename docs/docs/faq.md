# FAQ
## Does the information on the page update automatically?
Yes! That's the whole point of Dynacat

## Can I create my own widgets?

Yes, there are multiple ways to create custom widgets:
* `iframe` widget - allows you to embed things from other websites
* `html` widget - allows you to insert your own static HTML
* `extension` widget - fetch HTML from a URL
* `custom-api` widget - fetch JSON from a URL and render it using custom HTML

## How can I change the title of a widget?

The title of all widgets can be changed by specifying the `title` property in the widget's configuration:

```yaml
- type: rss
  title: My custom title

- type: markets
  title: My custom title

- type: videos
  title: My custom title

# and so on for all widgets...
```

## I get an error: dynacat.yml: no such file or directory

Rename your file `glance.yml` into `dynacat.yml` if you are transitioning from Glance. 

If you're not make sure you copied the installation command correctly:

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