package compose

import (
	"strings"
	"testing"
)

// BUGS L201: `docker compose` accepts any bind, any namespace and any env
// file, so a package from a source could mount an owner's store, `.env` or the
// docker socket and install cleanly. Every row below is one way out of the
// package's own ground, and the last block is what stays inside it.
func TestCheckSourcedFragment(t *testing.T) {
	const own = "services:\n  pkg:\n    image: x\n"
	reaches := map[string]string{
		"docker socket":       own + "    volumes: ['/var/run/docker.sock:/var/run/docker.sock']\n",
		"owner store":         own + "    volumes: ['${DATA_DIR}/store/routd:/store']\n",
		"traversal":           own + "    volumes: ['${DATA_DIR}/store/pkg/../routd:/store']\n",
		"whole data dir":      own + "    volumes: ['${DATA_DIR}:/all']\n",
		"shared .env":         own + "    volumes: ['${DATA_DIR}/.env:/x/.env:ro']\n",
		"another env var":     own + "    volumes: ['${HOST_DATA_DIR}/store/pkg:/store']\n",
		"literal host path":   own + "    volumes: ['/srv/data/x/store/pkg:/store']\n",
		"relative path":       own + "    volumes: ['../.env:/x/.env']\n",
		"long bind":           own + "    volumes:\n      - type: bind\n        source: /var/run/docker.sock\n        target: /var/run/docker.sock\n",
		"long volume as path": own + "    volumes:\n      - type: volume\n        source: /etc\n        target: /etc\n",
		"unknown mount type":  own + "    volumes:\n      - type: npipe\n        source: x\n        target: /x\n",
		"scoped env file":     own + "    env_file: ['../env/routd.env']\n",
		"own env file":        own + "    env_file: ../env/pkg.env\n",
		"privileged":          own + "    privileged: true\n",
		"cap_add":             own + "    cap_add: [SYS_ADMIN]\n",
		"devices":             own + "    devices: ['/dev/sda:/dev/sda']\n",
		"pid host":            own + "    pid: host\n",
		"network host":        own + "    network_mode: host\n",
		"network of routd":    own + "    network_mode: service:routd\n",
		"ipc host":            own + "    ipc: host\n",
		"userns host":         own + "    userns_mode: host\n",
		"cgroup host":         own + "    cgroup: host\n",
		"seccomp off":         own + "    security_opt: ['seccomp:unconfined']\n",
		"volumes_from":        own + "    volumes_from: [routd]\n",
		"extends":             own + "    extends:\n      file: ../docker-compose.yml\n      service: routd\n",
		"build":               own + "    build: ../\n",
		"include":             "include: [../docker-compose.yml]\n" + own,
		"volume driver_opts":  own + "    volumes: ['sock:/var/run/docker.sock']\nvolumes:\n  sock:\n    driver_opts: {type: none, o: bind, device: /var/run/docker.sock}\n",
		"volume driver":       own + "volumes:\n  sock:\n    driver: local\n",
		"config from file":    own + "configs:\n  c:\n    file: ../.env\n",
		"secret from env":     own + "secrets:\n  s:\n    environment: SECRETS_KEY\n",
		"foreign service":     "services:\n  routd:\n    image: x\n",
		"unreadable":          "services: [\n",
	}
	for label, fragment := range reaches {
		t.Run(label, func(t *testing.T) {
			err := CheckSourcedFragment("pkg", "pkg.yml", []byte(fragment))
			if err == nil {
				t.Fatalf("fragment passed:\n%s", fragment)
			}
			if !strings.HasPrefix(err.Error(), "pkg.yml: ") {
				t.Errorf("error does not name the file: %v", err)
			}
		})
	}

	stays := map[string]string{
		"own store dir":      own + "    volumes: ['${DATA_DIR}/store/pkg:/store']\n",
		"own subdir":         own + "    volumes: ['${DATA_DIR}/store/pkg/auth:/auth:ro']\n",
		"named volume":       own + "    volumes: ['pkg_db:/var/lib/db']\nvolumes:\n  pkg_db:\n",
		"external volume":    own + "    volumes: ['pkg_db:/var/lib/db']\nvolumes:\n  pkg_db:\n    external: true\n",
		"anonymous volume":   own + "    volumes: ['/var/cache']\n",
		"tmpfs":              own + "    volumes:\n      - type: tmpfs\n        target: /tmp\n",
		"long named volume":  own + "    volumes:\n      - type: volume\n        source: pkg_db\n        target: /db\n",
		"sibling service":    own + "  pkg_db:\n    image: postgres\n",
		"network none":       own + "    network_mode: none\n",
		"ipc shareable":      own + "    ipc: shareable\n",
		"no-new-privileges":  own + "    security_opt: ['no-new-privileges:true']\n",
		"inline config":      own + "configs:\n  c:\n    content: 'x: 1'\n",
		"interpolated value": own + "    environment:\n      KEY: '${PKG_KEY:?unset}'\n",
	}
	for label, fragment := range stays {
		t.Run(label, func(t *testing.T) {
			if err := CheckSourcedFragment("pkg", "pkg.yml", []byte(fragment)); err != nil {
				t.Fatalf("fragment refused: %v\n%s", err, fragment)
			}
		})
	}
}
