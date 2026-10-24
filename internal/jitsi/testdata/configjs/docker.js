// Jitsi Meet configuration.
var config = {};

config.hosts = {};

config.hosts.domain = 'meet.jitsi';
config.focusUserJid = 'focus@auth.meet.jitsi';

var subdir = '<!--# echo var="subdir" default="" -->';
var subdomain = "<!--# echo var="subdomain" default="" -->";
if (subdir.startsWith('<!--')) {
    subdir = '';
}
if (subdomain) {
    subdomain = subdomain.substring(0,subdomain.length-1).split('.').join('_').toLowerCase() + '.';
}
config.hosts.muc = 'muc.' + subdomain + 'meet.jitsi';
config.bosh = 'https://localhost:8443/' + subdir + 'http-bind';
config.websocket = 'wss://localhost:8443/' + subdir + 'xmpp-websocket';
// config.hosts.anonymousdomain = 'guest.meet.jitsi';
/* config.hosts.authdomain = 'meet.jitsi'; */
config.p2p.enabled = true;
