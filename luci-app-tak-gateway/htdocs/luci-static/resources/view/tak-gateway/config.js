'use strict';
'require view';
'require form';
'require uci';
'require fs';
'require ui';
'require poll';
'require dom';
'require tools.widgets as widgets';

const STATUS_FILE = '/var/run/tak-gateway.json';
const BIN = '/usr/bin/tak-gateway';

const TEAMS = [ 'White', 'Yellow', 'Orange', 'Magenta', 'Red', 'Maroon', 'Purple',
	'Dark Blue', 'Blue', 'Cyan', 'Teal', 'Green', 'Dark Green', 'Brown' ];
const ROLES = [ 'Team Member', 'Team Lead', 'HQ', 'Sniper', 'Medic',
	'Forward Observer', 'RTO', 'K9' ];
const TYPES = [
	[ 'a-f-G-U-C', 'Friendly ground unit' ],
	[ 'a-f-G-I-U-T', 'Friendly telecom / network node' ],
	[ 'a-f-G-E-S', 'Friendly sensor' ],
	[ 'a-f-G-I', 'Friendly installation' ],
	[ 'a-n-G', 'Neutral ground' ]
];

function ago(ts) {
	if (!ts)
		return '-';
	const s = Math.max(0, Math.round((Date.now() - new Date(ts).getTime()) / 1000));
	if (s < 60) return _('%ds ago').format(s);
	if (s < 3600) return _('%dm ago').format(Math.floor(s / 60));
	return _('%dh ago').format(Math.floor(s / 3600));
}

function badge(state) {
	const color = { connected: '#2e7d32', connecting: '#b58900', down: '#c62828', off: '#777' }[state] || '#777';
	return E('span', {
		'style': 'display:inline-block;padding:1px 8px;border-radius:9px;color:#fff;background:%s'.format(color)
	}, [ state || '-' ]);
}

function row(label, value) {
	return E('tr', { 'class': 'tr' }, [
		E('td', { 'class': 'td left', 'width': '33%' }, [ label ]),
		E('td', { 'class': 'td left' }, Array.isArray(value) ? value : [ value ])
	]);
}

function renderStatus(st) {
	if (!st)
		return E('p', {}, E('em', {}, _('The gateway is not running. Tick "Enable" below, then Save & Apply.')));

	const srv = st.server || {}, pos = st.position || {}, mesh = st.mesh || {}, c = st.counters || {};
	const srvCell = srv.enabled
		? [ badge(srv.state), ' ', srv.address || '', srv.error ? E('div', { 'style': 'color:#c62828' }, [ srv.error ]) : '' ]
		: [ badge('off'), ' ', _('mesh only') ];
	const posCell = pos.source === 'none'
		? E('em', {}, _('no position set - add one below so the gateway shows on the map'))
		: '%.5f, %.5f (%s)'.format(pos.lat, pos.lon, pos.source);

	const table = E('table', { 'class': 'table' }, [
		row(_('Callsign'), '%s  (%s)'.format(st.callsign, st.uid)),
		row(_('TAK Server'), srvCell),
		row(_('Position'), posCell),
		row(_('Bridged networks'), (mesh.interfaces || []).join(', ') || E('em', {}, _('none'))),
		row(_('Traffic'), _('mesh in %d / out %d, server in %d / out %d, relayed %d, duplicates dropped %d')
			.format(c.mesh_rx || 0, c.mesh_tx || 0, c.server_rx || 0, c.server_tx || 0, c.bridged || 0, c.duplicates || 0)),
		row(_('Running for'), '%t'.format(st.uptime || 0))
	]);

	const clients = st.clients || [];
	const ctable = E('table', { 'class': 'table' }, [
		E('tr', { 'class': 'tr table-titles' }, [
			E('th', { 'class': 'th' }, _('Callsign')),
			E('th', { 'class': 'th' }, _('Type')),
			E('th', { 'class': 'th' }, _('Position')),
			E('th', { 'class': 'th' }, _('Address')),
			E('th', { 'class': 'th' }, _('Last heard'))
		])
	].concat(clients.length ? clients.map(function(cl) {
		return E('tr', { 'class': 'tr' }, [
			E('td', { 'class': 'td' }, cl.callsign || cl.uid),
			E('td', { 'class': 'td' }, cl.type),
			E('td', { 'class': 'td' }, '%.5f, %.5f'.format(cl.lat, cl.lon)),
			E('td', { 'class': 'td' }, '%s (%s)'.format(cl.ip, cl.iface || '?')),
			E('td', { 'class': 'td' }, ago(cl.last_seen))
		]);
	}) : [ E('tr', { 'class': 'tr placeholder' }, E('td', { 'class': 'td', 'colspan': 5 }, E('em', {}, _('No TAK users heard on the local networks yet')))) ]));

	return E([], [ table, E('h4', {}, _('Local TAK users')), ctable ]);
}

function readStatus() {
	return L.resolveDefault(fs.read(STATUS_FILE), null).then(function(txt) {
		try { return txt ? JSON.parse(txt) : null; } catch (e) { return null; }
	});
}

return view.extend({
	load: function() {
		return Promise.all([ uci.load('tak-gateway'), uci.load('system'), readStatus() ]);
	},

	importCert: function(kind) {
		const path = kind === 'zip' ? '/tmp/tak-gateway-upload.zip' : '/tmp/tak-gateway-upload.p12';
		const pwInput = document.getElementById('tak-import-pw');
		const pw = pwInput ? pwInput.value : '';

		return ui.uploadFile(path).then(function() {
			const args = [ 'import' ];
			if (pw)
				args.push('-password', pw);
			args.push(path);
			return fs.exec(BIN, args);
		}).then(function(res) {
			fs.exec('/bin/rm', [ path ]).catch(function() {});
			let r;
			try { r = JSON.parse(res.stdout); }
			catch (e) { r = { ok: false, error: (res.stderr || res.stdout || _('no output')).trim() }; }
			if (!r.ok)
				throw new Error(r.error);

			if (r.host) uci.set('tak-gateway', 'server', 'host', r.host);
			if (r.port) uci.set('tak-gateway', 'server', 'port', r.port);
			if (r.protocol) uci.set('tak-gateway', 'server', 'protocol', r.protocol);
			if (r.cert) uci.set('tak-gateway', 'server', 'cert', r.cert);
			if (r.key) uci.set('tak-gateway', 'server', 'key', r.key);
			if (r.ca) uci.set('tak-gateway', 'server', 'ca', r.ca);
			uci.set('tak-gateway', 'server', 'enabled', '1');

			return uci.save().then(function() {
				ui.addNotification(null, E('p', [
					r.host ? _('Imported the connection to %s:%s.').format(r.host, r.port) : _('Imported the client certificate.'),
					' ', r.note || '', ' ',
					E('strong', {}, _('Press Save & Apply to use it.'))
				]), 'info');
				window.setTimeout(function() { window.location.reload(); }, 2500);
			});
		}).catch(function(e) {
			if (e && e.message && e.message.match(/cancel/i))
				return;
			ui.addNotification(null, E('p', _('Import failed: %s').format(e.message || e)), 'error');
		});
	},

	render: function(data) {
		const self = this;
		const hostname = uci.get('system', '@system[0]', 'hostname') || 'OpenWrt';
		let m, s, o;

		const statusBox = E('div', { 'id': 'tak-status' }, renderStatus(data[2]));
		poll.add(function() {
			return readStatus().then(function(st) {
				const box = document.getElementById('tak-status');
				if (box)
					dom.content(box, renderStatus(st));
			});
		}, 5);

		m = new form.Map('tak-gateway', _('TAK Gateway'),
			_('Puts this device on the TAK map and connects ATAK / iTAK / WinTAK users on your local networks to a TAK Server.'));

		/* ---- this device ---- */
		s = m.section(form.NamedSection, 'main', 'gateway', _('This device'));
		s.addremove = false;

		o = s.option(form.Flag, 'enabled', _('Enable'));
		o.rmempty = false;

		o = s.option(form.Value, 'callsign', _('Callsign'), _('Name shown on the TAK map.'));
		o.placeholder = hostname;

		o = s.option(form.ListValue, 'team', _('Team color'));
		TEAMS.forEach(function(t) { o.value(t, _(t)); });
		o.default = 'Cyan';

		o = s.option(form.ListValue, 'role', _('Role'));
		ROLES.forEach(function(r) { o.value(r, _(r)); });
		o.default = 'Team Member';

		o = s.option(form.Value, 'type', _('Map symbol'), _('CoT type. Pick one or type your own.'));
		TYPES.forEach(function(t) { o.value(t[0], '%s - %s'.format(t[0], _(t[1]))); });
		o.default = 'a-f-G-U-C';

		o = s.option(form.Value, 'lat', _('Latitude'), _('Decimal degrees, e.g. 38.8895'));
		o.datatype = 'range(-90,90)';
		o = s.option(form.Value, 'lon', _('Longitude'), _('Decimal degrees, e.g. -77.0353'));
		o.datatype = 'range(-180,180)';
		o = s.option(form.Value, 'hae', _('Height (m)'));
		o.datatype = 'float';
		o.optional = true;

		o = s.option(form.Flag, 'gpsd', _('Use GPS (gpsd)'), _('Follow a GPS through gpsd. The fixed position above is used when there is no fix.'));
		o = s.option(form.Value, 'gpsd_address', _('gpsd address'));
		o.placeholder = '127.0.0.1:2947';
		o.depends('gpsd', '1');

		o = s.option(form.Value, 'beacon', _('Report every (seconds)'));
		o.datatype = 'range(5,3600)';
		o.placeholder = '30';

		o = s.option(form.Value, 'remarks', _('Remarks'));
		o.optional = true;

		/* ---- TAK Server ---- */
		s = m.section(form.NamedSection, 'server', 'server', _('TAK Server'),
			_('Leave disabled to only bridge the local networks (no server).'));
		s.addremove = false;

		o = s.option(form.Flag, 'enabled', _('Connect to a TAK Server'));
		o.rmempty = false;

		o = s.option(form.DummyValue, '_import', _('Quick setup'));
		o.cfgvalue = function() { return ''; };
		o.write = o.remove = function() {};
		o.renderWidget = function() {
			return E('div', {}, [
				E('input', { 'id': 'tak-import-pw', 'type': 'password', 'class': 'cbi-input-password',
					'placeholder': _('certificate password (if not in the package)'), 'style': 'margin-bottom:6px' }),
				E('br'),
				E('button', { 'class': 'cbi-button cbi-button-action', 'click': ui.createHandlerFn(self, 'importCert', 'zip') },
					_('Import data package (.zip)')),
				' ',
				E('button', { 'class': 'cbi-button', 'click': ui.createHandlerFn(self, 'importCert', 'p12') },
					_('Import client certificate (.p12)')),
				E('div', { 'class': 'cbi-value-description' },
					_('Use the connection data package your TAK Server admin gave you; it fills in everything below.'))
			]);
		};

		o = s.option(form.Value, 'host', _('Server address'));
		o.datatype = 'host';
		o.placeholder = 'tak.example.org';

		o = s.option(form.Value, 'port', _('Port'));
		o.datatype = 'port';
		o.placeholder = '8089';

		o = s.option(form.ListValue, 'protocol', _('Connection'));
		o.value('ssl', _('SSL / TLS with client certificate (usually 8089)'));
		o.value('tcp', _('Plain TCP, not encrypted (usually 8087)'));
		o.default = 'ssl';

		o = s.option(form.Value, 'cert', _('Client certificate (PEM)'));
		o.depends('protocol', 'ssl');
		o = s.option(form.Value, 'key', _('Client key (PEM)'));
		o.depends('protocol', 'ssl');
		o = s.option(form.Value, 'ca', _('Server CA (PEM)'));
		o.depends('protocol', 'ssl');
		o = s.option(form.Flag, 'insecure', _('Skip server certificate check'),
			_('Only for testing; the connection can then be intercepted.'));
		o.depends('protocol', 'ssl');

		/* ---- local networks ---- */
		s = m.section(form.NamedSection, 'mesh', 'mesh', _('Local TAK users'),
			_('TAK apps on these networks are heard on SA multicast 239.2.3.1:6969 and GeoChat 224.10.10.1:17012.'));
		s.addremove = false;

		o = s.option(form.Flag, 'enabled', _('Bridge local networks'));
		o.rmempty = false;
		o.default = '1';

		o = s.option(widgets.NetworkSelect, 'network', _('Networks'), _('e.g. lan, your mesh or HaLow network.'));
		o.multiple = true;
		o.nocreate = true;
		o.depends('enabled', '1');

		o = s.option(form.DynamicList, 'device', _('Extra devices'), _('Optional raw interface names, e.g. wlan0-mesh.'));
		o.optional = true;
		o.depends('enabled', '1');

		o = s.option(form.Flag, 'to_server', _('Send local traffic to the server'));
		o.default = '1';
		o.depends('enabled', '1');
		o = s.option(form.Flag, 'from_server', _('Send server traffic to local users'));
		o.default = '1';
		o.depends('enabled', '1');
		o = s.option(form.Flag, 'bridge', _('Relay between the selected networks'));
		o.default = '1';
		o.depends('enabled', '1');
		o = s.option(form.Flag, 'chat', _('Include GeoChat'));
		o.default = '1';
		o.depends('enabled', '1');

		o = s.option(form.DynamicList, 'from_server_types', _('Only these types from the server'),
			_('CoT type prefixes, e.g. a- for units and b-t-f for chat. Empty sends everything.'));
		o.optional = true;
		o.depends('from_server', '1');

		o = s.option(form.Value, 'ttl', _('Multicast TTL'));
		o.datatype = 'range(1,16)';
		o.placeholder = '1';
		o.depends('enabled', '1');

		return m.render().then(function(formEl) {
			return E([], [
				E('div', { 'class': 'cbi-section' }, [ E('h3', {}, _('Status')), statusBox ]),
				formEl
			]);
		});
	}
});
