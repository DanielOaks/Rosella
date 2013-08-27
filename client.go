package main

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

func (c *Client) setNick(nick string) {
	if c.nick != "" {
		oldNickKey := strings.ToLower(c.nick)
		delete(c.server.clientMap, oldNickKey)
		for _, channel := range c.channelMap {
			delete(channel.clientMap, oldNickKey)
		}
	}

	//Set up new nick
	oldNick := c.nick
	c.nick = nick
	nickKey := strings.ToLower(c.nick)
	c.server.clientMap[nickKey] = c

	clients := make([]string, 0, 100)

	for _, channel := range c.channelMap {
		channel.clientMap[nickKey] = c

		//Collect list of client nicks who can see us
		for _, client := range channel.clientMap {
			clients = append(clients, client.nick)
		}
	}

	//By sorting the nicks and skipping duplicates we send each client one message
	sort.Strings(clients)
	prevNick := ""
	for _, nick := range clients {
		if nick == prevNick {
			continue
		}
		prevNick = nick

		client, exists := c.server.clientMap[strings.ToLower(nick)]
		if exists {
			client.reply(rplNickChange, oldNick, c.nick)
		}
	}
}

func (c *Client) joinChannel(channelName string) {
	newChannel := false

	channelKey := strings.ToLower(channelName)
	channel, exists := c.server.channelMap[channelKey]
	if exists == false {
		channel = &Channel{name: channelName,
			topic:     "",
			clientMap: make(map[string]*Client),
			modeMap:   make(map[string]*ClientMode)}
		c.server.channelMap[channelKey] = channel
		newChannel = true
	}

	nickKey := strings.ToLower(c.nick)

	if _, inChannel := channel.clientMap[nickKey]; inChannel {
		//Client is already in the channel, do nothing
		return
	}

	mode := new(ClientMode)
	if newChannel {
		//If they created the channel, make them op
		mode.operator = true
	}

	channel.clientMap[nickKey] = c
	channel.modeMap[nickKey] = mode
	c.channelMap[channelKey] = channel

	for _, client := range channel.clientMap {
		client.reply(rplJoin, c.nick, channelName)
	}

	if channel.topic != "" {
		c.reply(rplTopic, channelName, channel.topic)
	} else {
		c.reply(rplNoTopic, channelName)
	}

	nicks := make([]string, 0, 100)
	for _, client := range channel.clientMap {
		prefix := ""

		nickKey := strings.ToLower(client.nick)
		if mode, exists := channel.modeMap[nickKey]; exists {
			if mode.operator {
				prefix = "@"
			} else if mode.voice {
				prefix = "+"
			}
		}

		nicks = append(nicks, fmt.Sprintf("%s%s", prefix, client.nick))
	}

	c.reply(rplNames, channelName, strings.Join(nicks, " "))
}

func (c *Client) partChannel(channelName string) {
	channelKey := strings.ToLower(channelName)
	channel, exists := c.server.channelMap[channelKey]
	if exists == false {
		return
	}

	if _, inChannel := channel.clientMap[strings.ToLower(c.nick)]; inChannel == false {
		//Client isn't in this channel, do nothing
		return
	}

	//Notify clients of the part
	for _, client := range channel.clientMap {
		client.reply(rplPart, c.nick, channelName)
	}

	delete(channel.clientMap, strings.ToLower(c.nick))

	if len(channel.clientMap) == 0 {
		delete(c.channelMap, channelKey)
	}
}

func (c *Client) disconnect() {
	c.connected = false
	c.signalChan <- signalStop
}

//Send a reply to a user with the code specified
func (c *Client) reply(code replyCode, args ...string) {
	if c.connected == false {
		return
	}

	switch code {
	case rplWelcome:
		c.outputChan <- fmt.Sprintf(":%s 001 %s :Welcome to %s", c.server.name, c.nick, c.server.name)
	case rplJoin:
		c.outputChan <- fmt.Sprintf(":%s JOIN %s", args[0], args[1])
	case rplPart:
		c.outputChan <- fmt.Sprintf(":%s PART %s", args[0], args[1])
	case rplTopic:
		c.outputChan <- fmt.Sprintf(":%s 332 %s %s :%s", c.server.name, c.nick, args[0], args[1])
	case rplNoTopic:
		c.outputChan <- fmt.Sprintf(":%s 331 %s %s :No topic is set", c.server.name, c.nick, args[0])
	case rplNames:
		//TODO: break long lists up into multiple messages
		c.outputChan <- fmt.Sprintf(":%s 353 %s = %s :%s", c.server.name, c.nick, args[0], args[1])
		c.outputChan <- fmt.Sprintf(":%s 366 %s", c.server.name, c.nick)
	case rplNickChange:
		c.outputChan <- fmt.Sprintf(":%s NICK %s", args[0], args[1])
	case rplKill:
		c.outputChan <- fmt.Sprintf(":%s KILL %s A %s", c.server.name, c.nick, args[0])
	case rplMsg:
		c.outputChan <- fmt.Sprintf(":%s PRIVMSG %s %s", args[0], args[1], args[2])
	case rplList:
		c.outputChan <- fmt.Sprintf(":%s 321 %s", c.server.name, c.nick)
		for _, listItem := range args {
			c.outputChan <- fmt.Sprintf(":%s 322 %s %s", c.server.name, c.nick, listItem)
		}
		c.outputChan <- fmt.Sprintf(":%s 323 %s", c.server.name, c.nick)
	case rplOper:
		c.outputChan <- fmt.Sprintf(":%s 381 %s :You are now an operator", c.server.name, c.nick)
	case rplChannelModeIs:
		c.outputChan <- fmt.Sprintf(":%s 324 %s %s %s", c.server.name, c.nick, args[0], args[1])
	case errMoreArgs:
		c.outputChan <- fmt.Sprintf(":%s 461 %s :Not enough params", c.server.name, c.nick)
	case errNoNick:
		c.outputChan <- fmt.Sprintf(":%s 431 %s :No nickname given", c.server.name, c.nick)
	case errInvalidNick:
		c.outputChan <- fmt.Sprintf(":%s 432 %s %s :Erronenous nickname", c.server.name, c.nick, args[0])
	case errNickInUse:
		c.outputChan <- fmt.Sprintf(":%s 433 %s %s :Nick already in use", c.server.name, c.nick, args[0])
	case errAlreadyReg:
		c.outputChan <- fmt.Sprintf(":%s 462 :You need a valid nick first", c.server.name)
	case errNoSuchNick:
		c.outputChan <- fmt.Sprintf(":%s 401 %s %s :No such nick/channel", c.server.name, c.nick, args[0])
	case errUnknownCommand:
		c.outputChan <- fmt.Sprintf(":%s 421 %s %s :Unknown command", c.server.name, c.nick, args[0])
	case errNotReg:
		c.outputChan <- fmt.Sprintf(":%s 451 :You have not registered", c.server.name)
	case errPassword:
		c.outputChan <- fmt.Sprintf(":%s 464 %s :Error, password incorrect", c.server.name, c.nick)
	case errNoPriv:
		c.outputChan <- fmt.Sprintf(":%s 481 %s :Permission denied", c.server.name, c.nick)
	}
}

func (c *Client) clientThread() {
	readSignalChan := make(chan signalCode, 3)
	writeSignalChan := make(chan signalCode, 3)
	writeChan := make(chan string, 100)

	go c.readThread(readSignalChan)
	go c.writeThread(writeSignalChan, writeChan)

	defer func() {
		//Part from all channels
		for channelName := range c.channelMap {
			c.partChannel(channelName)
		}

		delete(c.server.clientMap, strings.ToLower(c.nick))

		c.connection.Close()
	}()

	for {
		select {
		case signal := <-c.signalChan:
			if signal == signalStop {
				readSignalChan <- signalStop
				writeSignalChan <- signalStop
				return
			}
		case line := <-c.outputChan:
			select {
			case writeChan <- line:
				continue
			default:
				c.disconnect()
			}
		}
	}

}

func (c *Client) readThread(signalChan chan signalCode) {
	for {
		select {
		case signal := <-signalChan:
			if signal == signalStop {
				return
			}
		default:
			c.connection.SetReadDeadline(time.Now().Add(time.Second * 3))
			buf := make([]byte, 512)
			ln, err := c.connection.Read(buf)
			if err != nil {
				if err == io.EOF {
					c.disconnect()
					return
				}
				continue
			}

			rawLines := buf[:ln]
			lines := bytes.Split(rawLines, []byte("\r\n"))
			for _, line := range lines {
				if len(line) > 0 {
					c.server.eventChan <- Event{client: c, input: string(line)}
				}
			}
		}
	}
}

func (c *Client) writeThread(signalChan chan signalCode, outputChan chan string) {
	for {
		select {
		case signal := <-signalChan:
			if signal == signalStop {
				return
			}
		case output := <-outputChan:
			line := []byte(fmt.Sprintf("%s\r\n", output))

			c.connection.SetWriteDeadline(time.Now().Add(time.Second * 30))
			if _, err := c.connection.Write(line); err != nil {
				c.disconnect()
				return
			}
		}
	}
}
